package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// WorkflowService (feat/workflow-v0) compiles YAML playbooks into staged
// issue trees and supervises their transitions.
//
// Division of labor: the platform's stage barrier joins parallel steps and
// wakes humans; agents and members do the work on ordinary issues; this
// engine owns every transition decision. The engine is deliberately
// poll-driven (reconcile tick), touches no issue-status write path, and keeps
// all run state in the run root's metadata — the database stays
// schema-compatible with upstream for everything run-related.
type WorkflowService struct {
	Queries   *db.Queries
	TxStarter TxStarter
	Bus       *events.Bus
	TaskSvc   *TaskService
}

// workflowRunLocks serializes reconcile vs. control ops per run root.
// Package-level on purpose: the HTTP handler and the tick loop construct
// separate WorkflowService instances in the same process, and both must
// contend on the same lock. The engine runs single-instance (one server
// process owns the tick), so in-process locking is sufficient; state writes
// are still last-writer-wins JSON in metadata, and the reducer is idempotent
// against re-observation.
var workflowRunLocks sync.Map // root issue id string -> *sync.Mutex

func NewWorkflowService(q *db.Queries, tx TxStarter, bus *events.Bus, taskSvc *TaskService) *WorkflowService {
	return &WorkflowService{Queries: q, TxStarter: tx, Bus: bus, TaskSvc: taskSvc}
}

func (s *WorkflowService) lockRun(rootID pgtype.UUID) func() {
	key := util.UUIDToString(rootID)
	muAny, _ := workflowRunLocks.LoadOrStore(key, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// ---- Definitions ----

// PushDefinition validates YAML source and upserts the definition by its
// YAML-declared name.
func (s *WorkflowService) PushDefinition(ctx context.Context, wsID, createdBy pgtype.UUID, source string) (db.WorkflowDefinition, *WorkflowDef, error) {
	def, err := ParseWorkflowDef(source)
	if err != nil {
		return db.WorkflowDefinition{}, nil, err
	}
	existing, err := s.Queries.GetWorkflowDefinitionByName(ctx, db.GetWorkflowDefinitionByNameParams{
		WorkspaceID: wsID,
		Name:        def.Name,
	})
	if err == nil {
		row, uerr := s.Queries.UpdateWorkflowDefinitionSource(ctx, db.UpdateWorkflowDefinitionSourceParams{
			ID:          existing.ID,
			WorkspaceID: wsID,
			Source:      source,
		})
		return row, def, uerr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.WorkflowDefinition{}, nil, err
	}
	row, cerr := s.Queries.CreateWorkflowDefinition(ctx, db.CreateWorkflowDefinitionParams{
		WorkspaceID: wsID,
		Name:        def.Name,
		Source:      source,
		CreatedBy:   createdBy,
	})
	return row, def, cerr
}

// ---- Run lifecycle ----

// StartRun materializes a run: a root issue assigned to the initiator (so
// stage-completion wakes land in their inbox), run state pinned to the
// definition source at start time, and the first stage group activated.
func (s *WorkflowService) StartRun(ctx context.Context, defRow db.WorkflowDefinition, vars map[string]string, initiatorID pgtype.UUID) (db.Issue, error) {
	def, err := ParseWorkflowDef(defRow.Source)
	if err != nil {
		return db.Issue{}, fmt.Errorf("definition no longer parses: %w", err)
	}
	for _, v := range def.Vars {
		if strings.TrimSpace(vars[v]) == "" {
			return db.Issue{}, fmt.Errorf("missing required var %q", v)
		}
	}

	now := time.Now().UTC()
	state := &WorkflowRunState{
		Version:      1,
		Workflow:     def.Name,
		DefinitionID: util.UUIDToString(defRow.ID),
		DefSHA:       WorkflowDefSHA(defRow.Source),
		DefSource:    defRow.Source,
		Status:       WorkflowRunRunning,
		Vars:         vars,
		InitiatorID:  util.UUIDToString(initiatorID),
		Steps:        map[string]*WorkflowStepState{},
		StartedAt:    now,
		UpdatedAt:    now,
	}
	if def.Policies.RunDeadline != "" {
		if d, err := time.ParseDuration(def.Policies.RunDeadline); err == nil {
			deadline := now.Add(d)
			state.Deadline = &deadline
		}
	}

	title := def.Name
	if len(def.Vars) > 0 {
		var parts []string
		for _, v := range def.Vars {
			parts = append(parts, vars[v])
		}
		title = def.Name + " · " + strings.Join(parts, " ")
	}

	// Root issue: creator and assignee are the initiating member, status
	// in_progress for the lifetime of the run.
	root, err := s.createIssue(ctx, createIssueInput{
		workspaceID:  defRow.WorkspaceID,
		title:        title,
		description:  "",
		status:       "in_progress",
		assigneeType: "member",
		assigneeID:   initiatorID,
		creatorID:    initiatorID,
		originID:     defRow.ID,
	})
	if err != nil {
		return db.Issue{}, err
	}

	unlock := s.lockRun(root.ID)
	defer unlock()

	first := def.Stages()[0]
	if err := s.materializeSteps(ctx, root, def, state, stageKeys(def, first), ""); err != nil {
		return root, err
	}
	state.Frontier = stageKeys(def, first)
	if err := s.saveRunState(ctx, root.ID, root.WorkspaceID, state); err != nil {
		return root, err
	}
	s.refreshProgress(ctx, root, def, state)
	s.postRunComment(ctx, root, fmt.Sprintf("▶ Workflow **%s** started (def %s). Stage %d activated: %s.",
		def.Name, state.DefSHA, first, strings.Join(state.Frontier, ", ")))
	return root, nil
}

// ---- Reconcile ----

// ReconcileAll advances every live run. Called from the server's workflow
// tick; also safe to invoke ad hoc (tests, manual kick).
func (s *WorkflowService) ReconcileAll(ctx context.Context) {
	roots, err := s.Queries.ListWorkflowRunRoots(ctx)
	if err != nil {
		slog.Error("workflow reconcile: list run roots failed", "error", err)
		return
	}
	for _, row := range roots {
		if err := s.ReconcileRun(ctx, row.ID); err != nil {
			slog.Error("workflow reconcile failed",
				"root_issue", util.UUIDToString(row.ID), "error", err)
		}
	}
}

// ReconcileRun advances a single run if it has anything to do.
func (s *WorkflowService) ReconcileRun(ctx context.Context, rootID pgtype.UUID) error {
	unlock := s.lockRun(rootID)
	defer unlock()

	root, err := s.Queries.GetIssue(ctx, rootID)
	if err != nil {
		return fmt.Errorf("get root: %w", err)
	}
	state, err := ParseWorkflowRunState(root.Metadata)
	if err != nil || state == nil {
		return err
	}
	if state.Status != WorkflowRunRunning {
		return nil
	}
	def, err := ParseWorkflowDef(state.DefSource)
	if err != nil {
		return s.escalate(ctx, root, def, state, fmt.Sprintf("pinned definition no longer parses: %v", err))
	}

	snaps, err := s.snapshotSteps(ctx, root, def, state)
	if err != nil {
		return fmt.Errorf("snapshot steps: %w", err)
	}
	decision := AdvanceWorkflow(def, state, snaps, time.Now().UTC())
	return s.applyDecision(ctx, root, def, state, snaps, decision)
}

// snapshotSteps loads the latest attempt issue for every step that has one.
func (s *WorkflowService) snapshotSteps(ctx context.Context, root db.Issue, def *WorkflowDef, state *WorkflowRunState) (map[string]WorkflowStepSnapshot, error) {
	snaps := map[string]WorkflowStepSnapshot{}
	for key, st := range state.Steps {
		attempt := st.LatestAttempt()
		if attempt == nil {
			continue
		}
		issueID, err := util.ParseUUID(attempt.IssueID)
		if err != nil {
			continue
		}
		issue, err := s.Queries.GetIssue(ctx, issueID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue // deleted attempt; reducer escalates on missing frontier snapshot
			}
			return nil, err
		}
		snap := WorkflowStepSnapshot{
			IssueStatus: issue.Status,
			CreatedAt:   attempt.CreatedAt,
			Outputs:     map[string]string{},
		}
		stepDef := def.Step(key)
		if stepDef != nil && len(stepDef.Outputs) > 0 && len(issue.Metadata) > 0 {
			var meta map[string]any
			if err := json.Unmarshal(issue.Metadata, &meta); err == nil {
				for name := range stepDef.Outputs {
					if v, ok := meta[name]; ok {
						snap.Outputs[name] = metadataValueToString(v)
					}
				}
			}
		}
		if stepDef != nil && stepDef.Type == WorkflowStepTypeApproval && issue.Status == "cancelled" {
			if c, err := s.Queries.GetLatestMemberCommentForIssue(ctx, issue.ID); err == nil {
				snap.RejectReason = c.Content
			}
		}
		snaps[key] = snap
	}
	return snaps, nil
}

func (s *WorkflowService) applyDecision(ctx context.Context, root db.Issue, def *WorkflowDef, state *WorkflowRunState, snaps map[string]WorkflowStepSnapshot, decision WorkflowDecision) error {
	switch decision.Kind {
	case WorkflowDecideNone:
		return nil

	case WorkflowDecideActivate:
		if decision.LoopStep != "" {
			if st := state.Steps[decision.LoopStep]; st != nil {
				st.LoopsUsed++
			}
		}
		if err := s.materializeSteps(ctx, root, def, state, decision.Activate, decision.RejectReason); err != nil {
			return err
		}
		state.Frontier = decision.Activate
		state.UpdatedAt = time.Now().UTC()
		if err := s.saveRunState(ctx, root.ID, root.WorkspaceID, state); err != nil {
			return err
		}
		s.refreshProgress(ctx, root, def, state)
		s.postRunComment(ctx, root, "⏩ "+decision.Reason)
		return nil

	case WorkflowDecideComplete:
		state.Status = WorkflowRunDone
		state.StatusReason = decision.Reason
		state.Frontier = nil
		state.UpdatedAt = time.Now().UTC()
		if err := s.saveRunState(ctx, root.ID, root.WorkspaceID, state); err != nil {
			return err
		}
		s.setIssueStatus(ctx, root, "done")
		s.refreshProgress(ctx, root, def, state)
		s.postRunComment(ctx, root, fmt.Sprintf("✅ Workflow **%s** completed: %s", state.Workflow, decision.Reason))
		return nil

	case WorkflowDecideAttention:
		return s.escalate(ctx, root, def, state, decision.Reason)
	}
	return nil
}

// escalate parks the run for a human: status needs_attention, root issue
// blocked, and a comment that lands in the initiator's inbox (they are the
// root assignee + subscriber). Failure's terminal state is always a person.
func (s *WorkflowService) escalate(ctx context.Context, root db.Issue, def *WorkflowDef, state *WorkflowRunState, reason string) error {
	state.Status = WorkflowRunNeedsAttention
	state.StatusReason = reason
	state.UpdatedAt = time.Now().UTC()
	if err := s.saveRunState(ctx, root.ID, root.WorkspaceID, state); err != nil {
		return err
	}
	s.setIssueStatus(ctx, root, "blocked")
	if def != nil {
		s.refreshProgress(ctx, root, def, state)
	}
	s.postRunComment(ctx, root, fmt.Sprintf("⚠️ Workflow **%s** needs attention: %s\n\nFix the underlying issue, then resume the run (`multica workflow resume %s`).",
		state.Workflow, reason, util.UUIDToString(root.ID)))
	return nil
}

// ---- Control operations ----

var ErrWorkflowRunState = errors.New("run is not in a state that allows this operation")

func (s *WorkflowService) loadRunForControl(ctx context.Context, rootID pgtype.UUID) (db.Issue, *WorkflowRunState, error) {
	root, err := s.Queries.GetIssue(ctx, rootID)
	if err != nil {
		return db.Issue{}, nil, err
	}
	state, err := ParseWorkflowRunState(root.Metadata)
	if err != nil {
		return root, nil, err
	}
	if state == nil {
		return root, nil, fmt.Errorf("issue %s is not a workflow run root", util.UUIDToString(rootID))
	}
	return root, state, nil
}

// PauseRun stops advancement; in-flight step issues keep running.
func (s *WorkflowService) PauseRun(ctx context.Context, rootID pgtype.UUID) error {
	unlock := s.lockRun(rootID)
	defer unlock()
	root, state, err := s.loadRunForControl(ctx, rootID)
	if err != nil {
		return err
	}
	if state.Status != WorkflowRunRunning {
		return ErrWorkflowRunState
	}
	state.Status = WorkflowRunPaused
	state.UpdatedAt = time.Now().UTC()
	if err := s.saveRunState(ctx, root.ID, root.WorkspaceID, state); err != nil {
		return err
	}
	s.postRunComment(ctx, root, "⏸ Run paused. In-flight steps finish; nothing new starts until resume.")
	return nil
}

// ResumeRun restarts advancement from paused or needs_attention.
func (s *WorkflowService) ResumeRun(ctx context.Context, rootID pgtype.UUID) error {
	unlock := s.lockRun(rootID)
	defer unlock()
	root, state, err := s.loadRunForControl(ctx, rootID)
	if err != nil {
		return err
	}
	if state.Status != WorkflowRunPaused && state.Status != WorkflowRunNeedsAttention {
		return ErrWorkflowRunState
	}
	state.Status = WorkflowRunRunning
	state.StatusReason = ""
	state.UpdatedAt = time.Now().UTC()
	if err := s.saveRunState(ctx, root.ID, root.WorkspaceID, state); err != nil {
		return err
	}
	s.setIssueStatus(ctx, root, "in_progress")
	s.postRunComment(ctx, root, "▶ Run resumed.")
	return nil
}

// CancelRun ends the run and explicitly cancels open step issues (no
// cascading deletes anywhere in this codebase: dependent cleanup is the
// application's job).
func (s *WorkflowService) CancelRun(ctx context.Context, rootID pgtype.UUID) error {
	unlock := s.lockRun(rootID)
	defer unlock()
	root, state, err := s.loadRunForControl(ctx, rootID)
	if err != nil {
		return err
	}
	if state.Status == WorkflowRunDone || state.Status == WorkflowRunCancelled {
		return ErrWorkflowRunState
	}
	for _, st := range state.Steps {
		attempt := st.LatestAttempt()
		if attempt == nil {
			continue
		}
		issueID, err := util.ParseUUID(attempt.IssueID)
		if err != nil {
			continue
		}
		issue, err := s.Queries.GetIssue(ctx, issueID)
		if err != nil || issue.Status == "done" || issue.Status == "cancelled" {
			continue
		}
		if err := s.TaskSvc.CancelTasksForIssue(ctx, issue.ID); err != nil {
			slog.Warn("workflow cancel: cancel tasks failed", "issue", attempt.IssueID, "error", err)
		}
		s.setIssueStatus(ctx, issue, "cancelled")
	}
	state.Status = WorkflowRunCancelled
	state.UpdatedAt = time.Now().UTC()
	if err := s.saveRunState(ctx, root.ID, root.WorkspaceID, state); err != nil {
		return err
	}
	s.setIssueStatus(ctx, root, "cancelled")
	s.postRunComment(ctx, root, "🛑 Run cancelled. Open step issues were cancelled with it.")
	return nil
}

// EjectRun is the escape hatch: the engine lets go, the issue tree stays.
func (s *WorkflowService) EjectRun(ctx context.Context, rootID pgtype.UUID) error {
	unlock := s.lockRun(rootID)
	defer unlock()
	root, state, err := s.loadRunForControl(ctx, rootID)
	if err != nil {
		return err
	}
	if state.Status == WorkflowRunDone || state.Status == WorkflowRunCancelled || state.Status == WorkflowRunEjected {
		return ErrWorkflowRunState
	}
	state.Status = WorkflowRunEjected
	state.UpdatedAt = time.Now().UTC()
	if err := s.saveRunState(ctx, root.ID, root.WorkspaceID, state); err != nil {
		return err
	}
	s.setIssueStatus(ctx, root, "in_progress")
	s.postRunComment(ctx, root, "⏏️ Run ejected. The engine has let go — this is a plain issue tree now; promote and close sub-issues manually.")
	return nil
}

// RetryStep manually materializes a fresh attempt for one step and points the
// frontier at it. Valid from running or needs_attention (the "fix and retry"
// path); the run returns to running.
func (s *WorkflowService) RetryStep(ctx context.Context, rootID pgtype.UUID, stepKey string) error {
	unlock := s.lockRun(rootID)
	defer unlock()
	root, state, err := s.loadRunForControl(ctx, rootID)
	if err != nil {
		return err
	}
	if state.Status != WorkflowRunRunning && state.Status != WorkflowRunNeedsAttention {
		return ErrWorkflowRunState
	}
	def, err := ParseWorkflowDef(state.DefSource)
	if err != nil {
		return err
	}
	step := def.Step(stepKey)
	if step == nil {
		return fmt.Errorf("unknown step %q", stepKey)
	}
	if err := s.materializeSteps(ctx, root, def, state, []string{stepKey}, ""); err != nil {
		return err
	}
	state.Frontier = []string{stepKey}
	state.Status = WorkflowRunRunning
	state.StatusReason = ""
	state.UpdatedAt = time.Now().UTC()
	if err := s.saveRunState(ctx, root.ID, root.WorkspaceID, state); err != nil {
		return err
	}
	s.setIssueStatus(ctx, root, "in_progress")
	s.refreshProgress(ctx, root, def, state)
	s.postRunComment(ctx, root, fmt.Sprintf("🔁 Step %q manually retried.", stepKey))
	return nil
}

// ---- Materialization ----

type createIssueInput struct {
	workspaceID  pgtype.UUID
	title        string
	description  string
	status       string
	assigneeType string
	assigneeID   pgtype.UUID
	creatorID    pgtype.UUID
	parentID     pgtype.UUID
	stage        int
	originID     pgtype.UUID
	childMeta    map[string]any
}

// createIssue is the engine's single issue-creation path: tx-scoped counter +
// position + insert (+ child metadata marker), then the standard
// issue:created event and — for agent assignees — the standard task enqueue.
func (s *WorkflowService) createIssue(ctx context.Context, in createIssueInput) (db.Issue, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.Issue{}, err
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)

	number, err := qtx.IncrementIssueCounter(ctx, in.workspaceID)
	if err != nil {
		return db.Issue{}, fmt.Errorf("increment issue counter: %w", err)
	}
	position, err := issueposition.NextTopPosition(ctx, tx, in.workspaceID, in.status)
	if err != nil {
		return db.Issue{}, fmt.Errorf("next position: %w", err)
	}
	stage := pgtype.Int4{}
	if in.stage > 0 {
		stage = pgtype.Int4{Int32: int32(in.stage), Valid: true}
	}
	issue, err := qtx.CreateIssueWithOrigin(ctx, db.CreateIssueWithOriginParams{
		WorkspaceID:   in.workspaceID,
		Title:         in.title,
		Description:   pgtype.Text{String: in.description, Valid: in.description != ""},
		Status:        in.status,
		Priority:      "none",
		AssigneeType:  pgtype.Text{String: in.assigneeType, Valid: in.assigneeType != ""},
		AssigneeID:    in.assigneeID,
		CreatorType:   "member",
		CreatorID:     in.creatorID,
		ParentIssueID: in.parentID,
		Position:      position,
		Number:        number,
		OriginType:    pgtype.Text{String: "workflow", Valid: true},
		OriginID:      in.originID,
		Stage:         stage,
	})
	if err != nil {
		return db.Issue{}, fmt.Errorf("create issue: %w", err)
	}
	if len(in.childMeta) > 0 {
		// Store as a JSON *string* value, not a nested object: issue.metadata is
		// a primitives-only KV on the API boundary (see the client's
		// IssueMetadataSchema), so a nested object would make the whole issue
		// fail schema parsing on older/stricter clients.
		raw, err := jsonStringValue(in.childMeta)
		if err != nil {
			return db.Issue{}, fmt.Errorf("encode child metadata: %w", err)
		}
		issue, err = qtx.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
			ID:          issue.ID,
			WorkspaceID: in.workspaceID,
			Key:         WorkflowChildMetaKey,
			Value:       raw,
		})
		if err != nil {
			return db.Issue{}, fmt.Errorf("set child metadata: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return db.Issue{}, err
	}

	prefix := s.issuePrefix(in.workspaceID)
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueCreated,
		WorkspaceID: util.UUIDToString(in.workspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload:     map[string]any{"issue": issueToMap(issue, prefix)},
	})

	switch in.assigneeType {
	case "agent":
		if _, err := s.TaskSvc.EnqueueTaskForIssue(ctx, issue); err != nil {
			return issue, fmt.Errorf("enqueue agent task: %w", err)
		}
	case "member":
		s.notifyMemberAssigned(ctx, issue, in.assigneeID)
	}
	return issue, nil
}

// materializeSteps creates a fresh attempt issue for every listed step key.
func (s *WorkflowService) materializeSteps(ctx context.Context, root db.Issue, def *WorkflowDef, state *WorkflowRunState, keys []string, rejectReason string) error {
	scope := s.buildScope(ctx, root, def, state, rejectReason)
	initiatorID, err := util.ParseUUID(state.InitiatorID)
	if err != nil {
		return fmt.Errorf("bad initiator id in run state: %w", err)
	}
	prefix := s.issuePrefix(root.WorkspaceID)

	for _, key := range keys {
		step := def.Step(key)
		if step == nil {
			return fmt.Errorf("unknown step %q", key)
		}
		assigneeType, assigneeID, err := s.resolveAssignee(ctx, root.WorkspaceID, step.Assignee)
		if err != nil {
			return fmt.Errorf("step %q: %w", key, err)
		}
		if step.Type == WorkflowStepTypeApproval && assigneeType != "member" {
			return fmt.Errorf("step %q: approval steps must be assigned to a member", key)
		}

		st := state.Steps[key]
		if st == nil {
			st = &WorkflowStepState{}
			state.Steps[key] = st
		}
		n := len(st.Attempts) + 1

		title := step.Title
		if title == "" {
			title = fmt.Sprintf("%s · %s", def.Name, key)
		}
		title = InterpolateWorkflowTemplate(title, scope)
		if n > 1 {
			title = fmt.Sprintf("%s (attempt %d)", title, n)
		}

		description := s.buildStepDescription(root, def, step, scope, prefix, rejectReason)
		issue, err := s.createIssue(ctx, createIssueInput{
			workspaceID:  root.WorkspaceID,
			title:        title,
			description:  description,
			status:       "todo",
			assigneeType: assigneeType,
			assigneeID:   assigneeID,
			creatorID:    initiatorID,
			parentID:     root.ID,
			stage:        step.Stage,
			originID:     root.ID,
			childMeta: map[string]any{
				"run_root": util.UUIDToString(root.ID),
				"step":     key,
				"attempt":  n,
			},
		})
		if err != nil {
			return fmt.Errorf("materialize step %q: %w", key, err)
		}
		st.Attempts = append(st.Attempts, WorkflowStepAttempt{
			IssueID:   util.UUIDToString(issue.ID),
			N:         n,
			CreatedAt: time.Now().UTC(),
		})
	}
	return nil
}

// buildScope assembles the interpolation scope from vars and every completed
// step's outputs/outcome.
func (s *WorkflowService) buildScope(ctx context.Context, root db.Issue, def *WorkflowDef, state *WorkflowRunState, rejectReason string) map[string]string {
	scope := map[string]string{}
	for k, v := range state.Vars {
		scope["vars."+k] = v
	}
	prefix := s.issuePrefix(root.WorkspaceID)
	scope["run.root_issue"] = fmt.Sprintf("%s-%d", prefix, root.Number)
	if rejectReason != "" {
		scope["reject_reason"] = rejectReason
	}
	for key, st := range state.Steps {
		attempt := st.LatestAttempt()
		if attempt == nil {
			continue
		}
		issueID, err := util.ParseUUID(attempt.IssueID)
		if err != nil {
			continue
		}
		issue, err := s.Queries.GetIssue(ctx, issueID)
		if err != nil {
			continue
		}
		stepDef := def.Step(key)
		if stepDef == nil {
			continue
		}
		if issue.Status == "done" || issue.Status == "cancelled" {
			scope["steps."+key+".outcome"] = workflowStepOutcome(stepDef, WorkflowStepSnapshot{IssueStatus: issue.Status})
		}
		if stepDef.Type == WorkflowStepTypeApproval && issue.Status == "cancelled" {
			if c, err := s.Queries.GetLatestMemberCommentForIssue(ctx, issue.ID); err == nil {
				scope["steps."+key+".reject_reason"] = c.Content
			}
		}
		if len(stepDef.Outputs) > 0 && len(issue.Metadata) > 0 {
			var meta map[string]any
			if err := json.Unmarshal(issue.Metadata, &meta); err == nil {
				for name := range stepDef.Outputs {
					if v, ok := meta[name]; ok {
						scope["steps."+key+".outputs."+name] = metadataValueToString(v)
					}
				}
			}
		}
	}
	return scope
}

func (s *WorkflowService) buildStepDescription(root db.Issue, def *WorkflowDef, step *WorkflowStepDef, scope map[string]string, prefix string, rejectReason string) string {
	var b strings.Builder
	if step.Type == WorkflowStepTypeApproval {
		b.WriteString("## Approval required\n\n")
		if step.Prompt != "" {
			b.WriteString(InterpolateWorkflowTemplate(step.Prompt, scope))
			b.WriteString("\n\n")
		}
		b.WriteString("- **Approve**: set this issue's status to **Done**.\n")
		b.WriteString("- **Reject**: set this issue's status to **Cancelled**, and leave a comment explaining why — your comment becomes the rework instruction.\n")
	} else {
		b.WriteString(InterpolateWorkflowTemplate(step.Prompt, scope))
		b.WriteString("\n")
	}
	if rejectReason != "" {
		b.WriteString("\n## Rework context\n\nThe previous pass was rejected with this reason:\n\n> ")
		b.WriteString(strings.ReplaceAll(rejectReason, "\n", "\n> "))
		b.WriteString("\n")
	}
	if len(step.Outputs) > 0 {
		b.WriteString("\n## Required outputs\n\nWhen you finish, record each output on THIS issue before setting it to done:\n\n")
		identifier := fmt.Sprintf("%s-%d", prefix, 0) // placeholder; replaced below with real number at create time
		_ = identifier
		for name, enum := range step.Outputs {
			if len(enum) > 0 {
				b.WriteString(fmt.Sprintf("- `multica issue metadata set <this-issue-id> %s --value \"<%s>\"`\n", name, strings.Join(enum, "|")))
			} else {
				b.WriteString(fmt.Sprintf("- `multica issue metadata set <this-issue-id> %s --value \"...\"`\n", name))
			}
		}
		b.WriteString("\nThe workflow engine routes on these values; a missing or off-enum value takes the default branch.\n")
	}
	b.WriteString(fmt.Sprintf("\n---\n*Step `%s` of workflow **%s** · run root %s-%d · transitions are engine-managed — do not promote sibling issues manually.*\n",
		step.Key, def.Name, prefix, root.Number))
	return b.String()
}

// ---- Assignee resolution ----

// resolveAssignee maps "agent:Name" / "member:Name" / bare "Name" (agent
// first, then member by user name or email) to an assignee pair. Matching is
// case-insensitive exact — fuzzy matching at run time would make transitions
// nondeterministic.
func (s *WorkflowService) resolveAssignee(ctx context.Context, wsID pgtype.UUID, ref string) (string, pgtype.UUID, error) {
	ref = strings.TrimSpace(ref)
	kind, name := "", ref
	if strings.HasPrefix(ref, "agent:") {
		kind, name = "agent", strings.TrimSpace(strings.TrimPrefix(ref, "agent:"))
	} else if strings.HasPrefix(ref, "member:") {
		kind, name = "member", strings.TrimSpace(strings.TrimPrefix(ref, "member:"))
	}

	if kind == "" || kind == "agent" {
		agents, err := s.Queries.ListAgents(ctx, wsID)
		if err != nil {
			return "", pgtype.UUID{}, err
		}
		for _, a := range agents {
			if strings.EqualFold(a.Name, name) && !a.ArchivedAt.Valid {
				return "agent", a.ID, nil
			}
		}
		if kind == "agent" {
			return "", pgtype.UUID{}, fmt.Errorf("no agent named %q", name)
		}
	}
	members, err := s.Queries.ListMembersWithUser(ctx, wsID)
	if err != nil {
		return "", pgtype.UUID{}, err
	}
	for _, m := range members {
		if strings.EqualFold(m.UserName, name) || strings.EqualFold(m.UserEmail, name) {
			return "member", m.UserID, nil
		}
	}
	return "", pgtype.UUID{}, fmt.Errorf("no agent or member named %q", name)
}

// ---- State persistence & observability ----

// ParseWorkflowRunState extracts run state from issue metadata; (nil, nil)
// when the issue is not a run root.
func ParseWorkflowRunState(metadata []byte) (*WorkflowRunState, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return nil, err
	}
	raw, ok := meta[WorkflowStateMetaKey]
	if !ok {
		return nil, nil
	}
	// The state may be stored as a JSON object or as a JSON string containing
	// the object (CLI writers string-encode); accept both.
	var state WorkflowRunState
	if err := json.Unmarshal(raw, &state); err == nil && state.Version > 0 {
		return &state, nil
	}
	var s2 string
	if err := json.Unmarshal(raw, &s2); err == nil {
		if err := json.Unmarshal([]byte(s2), &state); err == nil && state.Version > 0 {
			return &state, nil
		}
	}
	return nil, fmt.Errorf("unparseable workflow_state")
}

func (s *WorkflowService) saveRunState(ctx context.Context, rootID, wsID pgtype.UUID, state *WorkflowRunState) error {
	// Stored as a JSON *string* value: issue.metadata is a primitives-only KV
	// on the API boundary, so the run-state object is string-encoded to keep
	// the run root issue parseable by every client. ParseWorkflowRunState
	// accepts both the string and legacy object encodings.
	raw, err := jsonStringValue(state)
	if err != nil {
		return err
	}
	_, err = s.Queries.SetIssueMetadataKey(ctx, db.SetIssueMetadataKeyParams{
		ID:          rootID,
		WorkspaceID: wsID,
		Key:         WorkflowStateMetaKey,
		Value:       raw,
	})
	return err
}

// jsonStringValue marshals v to JSON, then encodes that JSON as a JSON string
// literal — the value written under an issue.metadata key so nested structures
// survive as a single primitive string rather than an object the API's
// primitives-only metadata schema would reject.
func jsonStringValue(v any) ([]byte, error) {
	inner, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(inner))
}

// refreshProgress rewrites the run root's description progress block — the
// zero-frontend-cost run banner. Best-effort: progress rendering must never
// fail a transition.
func (s *WorkflowService) refreshProgress(ctx context.Context, root db.Issue, def *WorkflowDef, state *WorkflowRunState) {
	block := s.renderProgressBlock(ctx, def, state)
	current := ""
	if root.Description.Valid {
		current = root.Description.String
	}
	updated := replaceAnchoredBlock(current, block)
	issue, err := s.Queries.UpdateWorkflowIssueDescription(ctx, db.UpdateWorkflowIssueDescriptionParams{
		ID:          root.ID,
		Description: pgtype.Text{String: updated, Valid: true},
	})
	if err != nil {
		slog.Warn("workflow progress update failed", "root", util.UUIDToString(root.ID), "error", err)
		return
	}
	s.broadcastIssueUpdated(issue, root.Status)
}

const workflowProgressStart = "<!-- workflow-progress:start -->"
const workflowProgressEnd = "<!-- workflow-progress:end -->"

func replaceAnchoredBlock(description, block string) string {
	wrapped := workflowProgressStart + "\n" + block + "\n" + workflowProgressEnd
	start := strings.Index(description, workflowProgressStart)
	end := strings.Index(description, workflowProgressEnd)
	if start >= 0 && end > start {
		return description[:start] + wrapped + description[end+len(workflowProgressEnd):]
	}
	if strings.TrimSpace(description) == "" {
		return wrapped
	}
	return description + "\n\n" + wrapped
}

func (s *WorkflowService) renderProgressBlock(ctx context.Context, def *WorkflowDef, state *WorkflowRunState) string {
	var b strings.Builder
	statusLabel := map[string]string{
		WorkflowRunRunning:        "▶ running",
		WorkflowRunPaused:         "⏸ paused",
		WorkflowRunNeedsAttention: "⚠️ needs attention",
		WorkflowRunEjected:        "⏏️ ejected",
		WorkflowRunDone:           "✅ done",
		WorkflowRunCancelled:      "🛑 cancelled",
	}[state.Status]
	b.WriteString(fmt.Sprintf("**Workflow %s** · %s · started %s\n\n", state.Workflow, statusLabel, state.StartedAt.Format("2006-01-02 15:04 MST")))
	if state.StatusReason != "" {
		b.WriteString(fmt.Sprintf("> %s\n\n", state.StatusReason))
	}
	b.WriteString("| Stage | Step | Assignee | Status | Attempt |\n|---|---|---|---|---|\n")
	frontier := map[string]bool{}
	for _, k := range state.Frontier {
		frontier[k] = true
	}
	for i := range def.Steps {
		step := &def.Steps[i]
		st := state.Steps[step.Key]
		icon, attemptCell := "○ pending", "—"
		if st != nil && st.LatestAttempt() != nil {
			attempt := st.LatestAttempt()
			attemptCell = strconv.Itoa(attempt.N)
			issueID, perr := util.ParseUUID(attempt.IssueID)
			status := ""
			if perr == nil {
				if issue, err := s.Queries.GetIssue(ctx, issueID); err == nil {
					status = issue.Status
					prefix := s.issuePrefix(issue.WorkspaceID)
					attemptCell = fmt.Sprintf("%s-%d (#%d)", prefix, issue.Number, attempt.N)
				}
			}
			switch status {
			case "done":
				icon = "✅ done"
				if step.Type == WorkflowStepTypeApproval {
					icon = "✅ approved"
				}
			case "cancelled":
				icon = "❌ failed"
				if step.Type == WorkflowStepTypeApproval {
					icon = "❌ rejected"
				}
			default:
				if frontier[step.Key] {
					icon = "◐ in progress"
				} else {
					icon = "◌ " + status
				}
			}
		}
		b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s |\n", step.Stage, step.Key, step.Assignee, icon, attemptCell))
	}
	return b.String()
}

// postRunComment writes a system comment on the run root (the transition
// log) and publishes comment:created so open clients refresh.
func (s *WorkflowService) postRunComment(ctx context.Context, root db.Issue, content string) {
	comment, err := s.Queries.CreateComment(ctx, db.CreateCommentParams{
		IssueID:     root.ID,
		WorkspaceID: root.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true}, // zero UUID: NOT NULL column, frontend branches on author_type
		Content:     content,
		Type:        "system",
	})
	if err != nil {
		slog.Warn("workflow comment failed", "root", util.UUIDToString(root.ID), "error", err)
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventCommentCreated,
		WorkspaceID: util.UUIDToString(root.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"comment": map[string]any{
				"id":          util.UUIDToString(comment.ID),
				"issue_id":    util.UUIDToString(comment.IssueID),
				"author_type": comment.AuthorType,
				"author_id":   util.UUIDToString(comment.AuthorID),
				"content":     comment.Content,
				"type":        comment.Type,
				"created_at":  util.TimestampToString(comment.CreatedAt),
			},
			"issue_title":  root.Title,
			"issue_status": root.Status,
		},
	})
}

// setIssueStatus is the engine's narrow status write + broadcast. It bypasses
// handler-side side effects on purpose: the engine polls outcomes instead of
// depending on notification chains, and run-root/step flips must not recurse
// into handler flows.
func (s *WorkflowService) setIssueStatus(ctx context.Context, issue db.Issue, status string) {
	if issue.Status == status {
		return
	}
	updated, err := s.Queries.UpdateWorkflowIssueStatus(ctx, db.UpdateWorkflowIssueStatusParams{
		ID:     issue.ID,
		Status: status,
	})
	if err != nil {
		slog.Warn("workflow status update failed", "issue", util.UUIDToString(issue.ID), "error", err)
		return
	}
	s.broadcastIssueUpdated(updated, issue.Status)
}

func (s *WorkflowService) broadcastIssueUpdated(issue db.Issue, prevStatus string) {
	prefix := s.issuePrefix(issue.WorkspaceID)
	s.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"issue":          issueToMap(issue, prefix),
			"status_changed": prevStatus != issue.Status,
			"prev_status":    prevStatus,
		},
	})
}

func (s *WorkflowService) notifyMemberAssigned(ctx context.Context, issue db.Issue, userID pgtype.UUID) {
	details, _ := json.Marshal(map[string]string{"reason": "workflow"})
	item, err := s.Queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
		WorkspaceID:   issue.WorkspaceID,
		RecipientType: "member",
		RecipientID:   userID,
		Type:          "issue_assigned",
		Severity:      "action_required",
		IssueID:       issue.ID,
		Title:         issue.Title,
		Body:          pgtype.Text{},
		ActorType:     pgtype.Text{String: "system", Valid: true},
		ActorID:       pgtype.UUID{Valid: true},
		Details:       details,
	})
	if err != nil {
		slog.Warn("workflow member inbox write failed", "issue", util.UUIDToString(issue.ID), "error", err)
		return
	}
	s.Bus.Publish(events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"inbox_item_id": util.UUIDToString(item.ID),
			"recipient": map[string]any{
				"type": "member",
				"id":   util.UUIDToString(userID),
			},
		},
	})
}

func (s *WorkflowService) issuePrefix(workspaceID pgtype.UUID) string {
	ws, err := s.Queries.GetWorkspace(context.Background(), workspaceID)
	if err != nil {
		return "ISSUE"
	}
	return ws.IssuePrefix
}

func metadataValueToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		raw, _ := json.Marshal(v)
		return string(raw)
	}
}
