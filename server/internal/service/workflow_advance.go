package service

import (
	"fmt"
	"time"
)

// Run state (feat/workflow-v0). Lives in the run root issue's metadata under
// key 'workflow_state' so the database stays schema-compatible with upstream:
// no run tables, no migrations for run data, and a run survives engine
// restarts because everything is reconstructed from the issue tree.

// Metadata keys.
const (
	// WorkflowStateMetaKey marks a run root and holds the WorkflowRunState JSON.
	WorkflowStateMetaKey = "workflow_state"
	// WorkflowChildMetaKey marks a step issue: {"run_root": id, "step": key, "attempt": n}.
	WorkflowChildMetaKey = "workflow"
)

// Run statuses.
const (
	WorkflowRunRunning        = "running"
	WorkflowRunPaused         = "paused"
	WorkflowRunNeedsAttention = "needs_attention"
	WorkflowRunEjected        = "ejected"
	WorkflowRunDone           = "done"
	WorkflowRunCancelled      = "cancelled"
)

type WorkflowRunState struct {
	Version      int               `json:"v"`
	Workflow     string            `json:"workflow"`
	DefinitionID string            `json:"definition_id,omitempty"`
	DefSHA       string            `json:"def_sha"`
	DefSource    string            `json:"def_source"`
	Status       string            `json:"status"`
	StatusReason string            `json:"status_reason,omitempty"`
	Vars         map[string]string `json:"vars,omitempty"`
	InitiatorID  string            `json:"initiator_id"`
	Frontier     []string          `json:"frontier"`
	// Activations counts materialization rounds. The Nth activation stamps
	// its step issues with issue.stage=N, so the sub-issue tree reads as the
	// execution timeline: a loop-back rework appears where it actually
	// happened instead of being folded back into its template stage. The
	// template stage lives in each child's workflow metadata and in the run
	// state; the engine itself never reads issue.stage.
	Activations int                           `json:"activations,omitempty"`
	Steps       map[string]*WorkflowStepState `json:"steps"`
	StartedAt   time.Time                     `json:"started_at"`
	Deadline    *time.Time                    `json:"deadline,omitempty"`
	UpdatedAt   time.Time                     `json:"updated_at"`
}

type WorkflowStepState struct {
	Attempts  []WorkflowStepAttempt `json:"attempts"`
	LoopsUsed int                   `json:"loops_used,omitempty"`
}

type WorkflowStepAttempt struct {
	IssueID   string    `json:"issue_id"`
	N         int       `json:"n"`
	CreatedAt time.Time `json:"created_at"`
}

// LatestAttempt returns the newest attempt or nil.
func (s *WorkflowStepState) LatestAttempt() *WorkflowStepAttempt {
	if s == nil || len(s.Attempts) == 0 {
		return nil
	}
	return &s.Attempts[len(s.Attempts)-1]
}

// WorkflowStepSnapshot is what the engine observed about a frontier step's
// latest attempt at reconcile time. Built from live issues by the service,
// consumed by the pure reducer below.
type WorkflowStepSnapshot struct {
	IssueStatus  string            // current issue status of the latest attempt
	CreatedAt    time.Time         // latest attempt creation time
	Outputs      map[string]string // declared outputs read from issue metadata
	RejectReason string            // last member comment (approval steps)
}

func (s WorkflowStepSnapshot) terminal() bool {
	return s.IssueStatus == "done" || s.IssueStatus == "cancelled"
}

// Step outcomes derived from a terminal snapshot.
const (
	workflowOutcomeSuccess  = "success"
	workflowOutcomeFailed   = "failed"
	workflowOutcomeApproved = "approved"
	workflowOutcomeRejected = "rejected"
)

func workflowStepOutcome(def *WorkflowStepDef, snap WorkflowStepSnapshot) string {
	if def.Type == WorkflowStepTypeApproval {
		if snap.IssueStatus == "done" {
			return workflowOutcomeApproved
		}
		return workflowOutcomeRejected
	}
	if snap.IssueStatus == "done" {
		return workflowOutcomeSuccess
	}
	return workflowOutcomeFailed
}

// WorkflowDecision is the reducer's verdict for one reconcile pass over one
// run. Exactly one Kind applies; Activate lists step keys to materialize as
// new attempts.
type WorkflowDecision struct {
	Kind WorkflowDecisionKind
	// Activate: step keys to create new attempts for (becomes the frontier).
	Activate []string
	// LoopStep: approval step whose loops_used must be bumped (Kind reject).
	LoopStep string
	// RejectReason propagated into rework prompts.
	RejectReason string
	// Reason: human-readable cause for attention / completion comments.
	Reason string
}

type WorkflowDecisionKind string

const (
	WorkflowDecideNone      WorkflowDecisionKind = "none"
	WorkflowDecideActivate  WorkflowDecisionKind = "activate"
	WorkflowDecideComplete  WorkflowDecisionKind = "complete"
	WorkflowDecideAttention WorkflowDecisionKind = "attention"
)

// AdvanceWorkflow is the pure transition function: given the pinned
// definition, the current run state, and a snapshot of every frontier step's
// latest attempt, decide what happens next. No I/O — the service materializes
// the decision. Determinism here is the whole point of the feature: agents
// and humans do the work, but transitions never depend on an LLM remembering
// to act.
func AdvanceWorkflow(def *WorkflowDef, state *WorkflowRunState, snaps map[string]WorkflowStepSnapshot, now time.Time) WorkflowDecision {
	if state.Status != WorkflowRunRunning {
		return WorkflowDecision{Kind: WorkflowDecideNone}
	}
	if state.Deadline != nil && now.After(*state.Deadline) {
		return WorkflowDecision{Kind: WorkflowDecideAttention, Reason: "run deadline exceeded"}
	}
	if len(state.Frontier) == 0 {
		// Defensive: a running run always has a frontier; treat as complete.
		return WorkflowDecision{Kind: WorkflowDecideComplete, Reason: "empty frontier"}
	}

	// Wait for the whole frontier to reach a terminal status, watching for
	// per-step timeouts while any attempt is still open.
	for _, key := range state.Frontier {
		stepDef := def.Step(key)
		snap, ok := snaps[key]
		if stepDef == nil || !ok {
			return WorkflowDecision{Kind: WorkflowDecideAttention, Reason: fmt.Sprintf("step %q has no live attempt (issue deleted or definition drift)", key)}
		}
		if !snap.terminal() {
			if now.Sub(snap.CreatedAt) > stepDef.StepTimeout() {
				return WorkflowDecision{Kind: WorkflowDecideAttention, Reason: fmt.Sprintf("step %q timed out after %s", key, stepDef.StepTimeout())}
			}
			return WorkflowDecision{Kind: WorkflowDecideNone}
		}
	}

	// Frontier fully terminal: classify outcomes.
	var rejected, failed, succeeded []string
	for _, key := range state.Frontier {
		switch workflowStepOutcome(def.Step(key), snaps[key]) {
		case workflowOutcomeRejected:
			rejected = append(rejected, key)
		case workflowOutcomeFailed:
			failed = append(failed, key)
		default:
			succeeded = append(succeeded, key)
		}
	}

	// Rejection first: an approval said no. Validate guarantees approvals are
	// alone in their stage, so at most one rejection is possible.
	if len(rejected) > 0 {
		key := rejected[0]
		stepDef := def.Step(key)
		if stepDef.OnReject == nil {
			return WorkflowDecision{Kind: WorkflowDecideAttention, Reason: fmt.Sprintf("approval %q rejected and no on_reject is declared", key)}
		}
		loops := 0
		if st := state.Steps[key]; st != nil {
			loops = st.LoopsUsed
		}
		if loops >= stepDef.OnReject.MaxLoops {
			return WorkflowDecision{Kind: WorkflowDecideAttention, Reason: fmt.Sprintf("approval %q rejected and max_loops (%d) is exhausted", key, stepDef.OnReject.MaxLoops)}
		}
		back := def.Step(stepDef.OnReject.BackTo)
		return WorkflowDecision{
			Kind:         WorkflowDecideActivate,
			Activate:     stageKeys(def, back.Stage),
			LoopStep:     key,
			RejectReason: snaps[key].RejectReason,
			Reason:       fmt.Sprintf("approval %q rejected; looping back to %q", key, stepDef.OnReject.BackTo),
		}
	}

	// Failures next. Retry budgets permitting, every failed step gets a fresh
	// attempt; a single failed step may instead reroute via on_fail.goto. Any
	// failure without a path escalates — the default outcome of failure is a
	// human, never a silent stall.
	if len(failed) > 0 {
		var retry []string
		for _, key := range failed {
			stepDef := def.Step(key)
			attempts := 0
			if st := state.Steps[key]; st != nil {
				attempts = len(st.Attempts)
			}
			switch {
			case stepDef.OnFail != nil && stepDef.OnFail.Retry > 0 && attempts-1 < stepDef.OnFail.Retry:
				retry = append(retry, key)
			case stepDef.OnFail != nil && stepDef.OnFail.Goto != "" && len(failed) == 1:
				return WorkflowDecision{
					Kind:     WorkflowDecideActivate,
					Activate: gotoTargetKeys(def, stepDef.OnFail.Goto),
					Reason:   fmt.Sprintf("step %q failed; routing to %q", key, stepDef.OnFail.Goto),
				}
			default:
				return WorkflowDecision{Kind: WorkflowDecideAttention, Reason: fmt.Sprintf("step %q failed with no retry budget left", key)}
			}
		}
		return WorkflowDecision{
			Kind:     WorkflowDecideActivate,
			Activate: retry,
			Reason:   fmt.Sprintf("retrying failed step(s) %v", retry),
		}
	}

	// All succeeded: route. Validate guarantees at most one router per stage.
	_ = succeeded
	for _, key := range state.Frontier {
		stepDef := def.Step(key)
		if stepDef.Next == nil {
			continue
		}
		n := stepDef.Next
		target := n.Goto
		reason := fmt.Sprintf("step %q routed to %q", key, target)
		if target == "" {
			m := workflowWhenRe.FindStringSubmatch(n.When)
			srcKey, outName := m[1], m[2]
			value := ""
			if snap, ok := snaps[srcKey]; ok {
				value = snap.Outputs[outName]
			}
			var routed bool
			if target, routed = n.Routes[value]; !routed {
				target = n.Default
				reason = fmt.Sprintf("output %s=%q matched no route; taking default %q", n.When, value, target)
			} else {
				reason = fmt.Sprintf("output %s=%q routed to %q", n.When, value, target)
			}
		}
		switch target {
		case WorkflowTargetDone:
			return WorkflowDecision{Kind: WorkflowDecideComplete, Reason: reason}
		case WorkflowTargetNeedsAttention:
			return WorkflowDecision{Kind: WorkflowDecideAttention, Reason: reason}
		default:
			return WorkflowDecision{
				Kind:     WorkflowDecideActivate,
				Activate: gotoTargetKeys(def, target),
				Reason:   reason,
			}
		}
	}

	// No router: fall through to the next stage group, or finish.
	maxStage := 0
	for _, key := range state.Frontier {
		if s := def.Step(key); s != nil && s.Stage > maxStage {
			maxStage = s.Stage
		}
	}
	next := def.NextStageAfter(maxStage)
	if next == 0 {
		return WorkflowDecision{Kind: WorkflowDecideComplete, Reason: "all stages complete"}
	}
	return WorkflowDecision{
		Kind:     WorkflowDecideActivate,
		Activate: stageKeys(def, next),
		Reason:   fmt.Sprintf("stage %d complete; advancing to stage %d", maxStage, next),
	}
}

// gotoTargetKeys resolves a jump target to the full stage group it lives in:
// routing to a step activates that step's parallel siblings too, keeping
// "steps sharing a stage run together" true under branching.
func gotoTargetKeys(def *WorkflowDef, target string) []string {
	step := def.Step(target)
	if step == nil {
		return nil
	}
	return stageKeys(def, step.Stage)
}

func stageKeys(def *WorkflowDef, stage int) []string {
	var out []string
	for _, s := range def.StageGroup(stage) {
		out = append(out, s.Key)
	}
	return out
}
