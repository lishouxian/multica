package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	maxPlaybookSteps      = 32
	defaultMaxAttempts    = 2
	defaultMaxTransitions = 32
	maximumMaxTransitions = 100
)

var playbookStepKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// PlaybookDefinition is the deliberately small, squad-private workflow format.
// Dependencies model both serial handoffs and wait-all joins. A condition on a
// dependency models an enum branch without adding a general expression engine.
type PlaybookDefinition struct {
	Version        int            `json:"version"`
	Start          string         `json:"start,omitempty"`
	MaxTransitions int            `json:"max_transitions,omitempty"`
	Steps          []PlaybookStep `json:"steps"`
}

type PlaybookStep struct {
	Key          string                  `json:"key"`
	Title        string                  `json:"title"`
	Instructions string                  `json:"instructions"`
	AgentID      string                  `json:"agent_id"`
	DependsOn    []string                `json:"depends_on,omitempty"`
	Input        map[string]InputBinding `json:"input,omitempty"`
	OutputSchema ValueSchema             `json:"output_schema"`
	When         *StepCondition          `json:"when,omitempty"`
	Transitions  []PlaybookTransition    `json:"transitions,omitempty"`
	MaxAttempts  int                     `json:"max_attempts,omitempty"`
}

type PlaybookTransition struct {
	To   string               `json:"to,omitempty"`
	End  bool                 `json:"end,omitempty"`
	When *TransitionCondition `json:"when,omitempty"`
}

type TransitionCondition struct {
	Field  string          `json:"field"`
	Equals json.RawMessage `json:"equals"`
}

type InputBinding struct {
	From    string          `json:"from,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	Default json.RawMessage `json:"default,omitempty"`
}

type StepCondition struct {
	Step   string          `json:"step"`
	Field  string          `json:"field"`
	Equals json.RawMessage `json:"equals"`
}

// ValueSchema is a restricted JSON-schema subset. It is intentionally small:
// object properties, required fields, primitive/array types, and enums cover
// handoff contracts without exposing arbitrary validation code.
type ValueSchema struct {
	Type                 string                 `json:"type"`
	Required             []string               `json:"required,omitempty"`
	Properties           map[string]ValueSchema `json:"properties,omitempty"`
	Items                *ValueSchema           `json:"items,omitempty"`
	Enum                 []json.RawMessage      `json:"enum,omitempty"`
	AdditionalProperties *bool                  `json:"additional_properties,omitempty"`
}

type PlaybookService struct {
	Queries      *db.Queries
	TxStarter    TxStarter
	IssueService *IssueService
}

func NewPlaybookService(q *db.Queries, tx TxStarter, issues *IssueService) *PlaybookService {
	return &PlaybookService{Queries: q, TxStarter: tx, IssueService: issues}
}

type PlaybookRunSnapshot struct {
	Run   db.WorkflowRun       `json:"run"`
	Nodes []db.WorkflowNodeRun `json:"nodes"`
}

type DispatchPlaybookStepParams struct {
	RunID         pgtype.UUID
	StepKey       string
	AgentID       pgtype.UUID
	InputOverride json.RawMessage
}

func DecodePlaybookDefinition(raw []byte) (PlaybookDefinition, error) {
	var definition PlaybookDefinition
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&definition); err != nil {
		return definition, fmt.Errorf("invalid playbook JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return definition, errors.New("invalid playbook JSON: multiple values")
	}
	if err := validatePlaybookShape(definition); err != nil {
		return definition, err
	}
	return definition, nil
}

func validatePlaybookShape(definition PlaybookDefinition) error {
	if definition.Version != 1 {
		return fmt.Errorf("playbook version must be 1")
	}
	if len(definition.Steps) == 0 || len(definition.Steps) > maxPlaybookSteps {
		return fmt.Errorf("playbook must contain between 1 and %d steps", maxPlaybookSteps)
	}
	steps := make(map[string]PlaybookStep, len(definition.Steps))
	stateMachine := definition.Start != ""
	for _, step := range definition.Steps {
		if !playbookStepKeyPattern.MatchString(step.Key) {
			return fmt.Errorf("invalid step key %q", step.Key)
		}
		if _, exists := steps[step.Key]; exists {
			return fmt.Errorf("duplicate step key %q", step.Key)
		}
		if strings.TrimSpace(step.Title) == "" {
			return fmt.Errorf("step %q requires title", step.Key)
		}
		if _, err := util.ParseUUID(step.AgentID); err != nil {
			return fmt.Errorf("step %q has invalid agent_id", step.Key)
		}
		if step.MaxAttempts < 0 || step.MaxAttempts > 10 {
			return fmt.Errorf("step %q max_attempts must be 0 (default) or between 1 and 10", step.Key)
		}
		if err := validateValueSchema(step.OutputSchema, "output_schema"); err != nil {
			return fmt.Errorf("step %q: %w", step.Key, err)
		}
		if len(step.Transitions) > 0 {
			stateMachine = true
		}
		steps[step.Key] = step
	}
	if stateMachine {
		if definition.Start == "" {
			return errors.New("state-machine playbook requires start")
		}
		if _, ok := steps[definition.Start]; !ok {
			return fmt.Errorf("playbook start references unknown step %q", definition.Start)
		}
		if definition.MaxTransitions < 0 || definition.MaxTransitions > maximumMaxTransitions {
			return fmt.Errorf("max_transitions must be 0 (default) or between 1 and %d", maximumMaxTransitions)
		}
	}

	for _, step := range definition.Steps {
		seenDependency := make(map[string]bool, len(step.DependsOn))
		for _, dependency := range step.DependsOn {
			if dependency == step.Key {
				return fmt.Errorf("step %q cannot depend on itself", step.Key)
			}
			if _, ok := steps[dependency]; !ok {
				return fmt.Errorf("step %q depends on unknown step %q", step.Key, dependency)
			}
			if seenDependency[dependency] {
				return fmt.Errorf("step %q repeats dependency %q", step.Key, dependency)
			}
			seenDependency[dependency] = true
		}
		if step.When != nil {
			if stateMachine {
				return fmt.Errorf("step %q cannot use dependency when in state-machine mode", step.Key)
			}
			if !seenDependency[step.When.Step] {
				return fmt.Errorf("step %q condition must reference a direct dependency", step.Key)
			}
			if strings.TrimSpace(step.When.Field) == "" || len(step.When.Equals) == 0 {
				return fmt.Errorf("step %q condition requires field and equals", step.Key)
			}
			var value any
			if err := json.Unmarshal(step.When.Equals, &value); err != nil {
				return fmt.Errorf("step %q condition equals is invalid JSON", step.Key)
			}
		}
		if stateMachine {
			if len(step.DependsOn) > 0 {
				return fmt.Errorf("step %q cannot use depends_on in state-machine mode", step.Key)
			}
			if len(step.Transitions) == 0 {
				return fmt.Errorf("step %q requires at least one transition", step.Key)
			}
			seenDefault := false
			for index, transition := range step.Transitions {
				if (transition.To == "") == !transition.End {
					return fmt.Errorf("step %q transition %d requires exactly one of to or end", step.Key, index)
				}
				if transition.To != "" {
					if _, ok := steps[transition.To]; !ok {
						return fmt.Errorf("step %q transitions to unknown step %q", step.Key, transition.To)
					}
				}
				if transition.When == nil {
					if seenDefault || index != len(step.Transitions)-1 {
						return fmt.Errorf("step %q unconditional transition must be unique and last", step.Key)
					}
					seenDefault = true
					continue
				}
				if seenDefault || strings.TrimSpace(transition.When.Field) == "" || len(transition.When.Equals) == 0 {
					return fmt.Errorf("step %q transition condition requires field and equals", step.Key)
				}
				var value any
				if err := json.Unmarshal(transition.When.Equals, &value); err != nil {
					return fmt.Errorf("step %q transition equals is invalid JSON", step.Key)
				}
			}
		}
		for name, binding := range step.Input {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("step %q has an empty input name", step.Key)
			}
			hasFrom := binding.From != ""
			hasValue := len(binding.Value) > 0
			if hasFrom == hasValue {
				return fmt.Errorf("step %q input %q requires exactly one of from or value", step.Key, name)
			}
			if hasFrom {
				parts := strings.Split(binding.From, ".")
				if len(parts) < 2 {
					return fmt.Errorf("step %q input %q has invalid from path", step.Key, name)
				}
				if parts[0] != "context" {
					_, declaredStep := steps[parts[0]]
					if (stateMachine && !declaredStep) || (!stateMachine && !seenDependency[parts[0]]) {
						return fmt.Errorf("step %q input %q reads unavailable step %q", step.Key, name, parts[0])
					}
				}
			}
			for _, raw := range []json.RawMessage{binding.Value, binding.Default} {
				if len(raw) == 0 {
					continue
				}
				var value any
				if err := json.Unmarshal(raw, &value); err != nil {
					return fmt.Errorf("step %q input %q contains invalid JSON", step.Key, name)
				}
			}
		}
	}

	if stateMachine {
		return nil
	}
	visiting := make(map[string]bool, len(steps))
	visited := make(map[string]bool, len(steps))
	var visit func(string) error
	visit = func(key string) error {
		if visiting[key] {
			return fmt.Errorf("playbook contains a cycle at step %q", key)
		}
		if visited[key] {
			return nil
		}
		visiting[key] = true
		for _, dependency := range steps[key].DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[key] = false
		visited[key] = true
		return nil
	}
	for key := range steps {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

func validateValueSchema(schema ValueSchema, path string) error {
	switch schema.Type {
	case "object":
		if schema.Properties == nil {
			return fmt.Errorf("%s object requires properties", path)
		}
		for _, required := range schema.Required {
			if _, ok := schema.Properties[required]; !ok {
				return fmt.Errorf("%s requires unknown property %q", path, required)
			}
		}
		for name, child := range schema.Properties {
			if err := validateValueSchema(child, path+"."+name); err != nil {
				return err
			}
		}
	case "array":
		if schema.Items == nil {
			return fmt.Errorf("%s array requires items", path)
		}
		if err := validateValueSchema(*schema.Items, path+"[]"); err != nil {
			return err
		}
	case "string", "number", "integer", "boolean", "null":
	default:
		return fmt.Errorf("%s has unsupported type %q", path, schema.Type)
	}
	for _, enumValue := range schema.Enum {
		var value any
		if err := json.Unmarshal(enumValue, &value); err != nil {
			return fmt.Errorf("%s contains invalid enum JSON", path)
		}
		if err := validateOutputValue(value, ValueSchema{Type: schema.Type, Properties: schema.Properties, Items: schema.Items}, path); err != nil {
			return fmt.Errorf("%s enum value does not match schema", path)
		}
	}
	return nil
}

func (s *PlaybookService) SaveDefinition(ctx context.Context, squad db.Squad, raw []byte) (db.WorkflowDefinition, error) {
	definition, err := DecodePlaybookDefinition(raw)
	if err != nil {
		return db.WorkflowDefinition{}, err
	}
	for _, step := range definition.Steps {
		agentID, _ := util.ParseUUID(step.AgentID)
		agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: squad.WorkspaceID})
		if err != nil || agent.ArchivedAt.Valid {
			return db.WorkflowDefinition{}, fmt.Errorf("step %q agent is unavailable in this workspace", step.Key)
		}
		isMember, err := s.Queries.IsSquadMember(ctx, db.IsSquadMemberParams{SquadID: squad.ID, MemberType: "agent", MemberID: agentID})
		if err != nil || !isMember {
			return db.WorkflowDefinition{}, fmt.Errorf("step %q agent must be a squad member", step.Key)
		}
	}

	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return db.WorkflowDefinition{}, fmt.Errorf("begin save playbook: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := s.Queries.WithTx(tx)
	row, err := qtx.UpsertSquadWorkflowDefinition(ctx, db.UpsertSquadWorkflowDefinitionParams{
		WorkspaceID: squad.WorkspaceID,
		SquadID:     squad.ID,
		Name:        squad.Name + " playbook",
		Definition:  raw,
	})
	if err != nil {
		return db.WorkflowDefinition{}, fmt.Errorf("save playbook: %w", err)
	}
	if _, err := qtx.EnableSquadPlaybook(ctx, db.EnableSquadPlaybookParams{ID: squad.ID, WorkflowDefinitionID: row.ID}); err != nil {
		return db.WorkflowDefinition{}, fmt.Errorf("enable playbook: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return db.WorkflowDefinition{}, fmt.Errorf("commit playbook: %w", err)
	}
	return row, nil
}

func (s *PlaybookService) StartRun(ctx context.Context, squad db.Squad, rootIssueID, createdBy pgtype.UUID, contextRaw []byte) (PlaybookRunSnapshot, error) {
	if squad.OrchestrationMode != "playbook" || !squad.WorkflowDefinitionID.Valid {
		return PlaybookRunSnapshot{}, errors.New("squad does not have an active playbook")
	}
	root, err := s.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: rootIssueID, WorkspaceID: squad.WorkspaceID})
	if err != nil {
		return PlaybookRunSnapshot{}, errors.New("root issue not found in squad workspace")
	}
	if !root.AssigneeType.Valid || root.AssigneeType.String != "squad" || root.AssigneeID != squad.ID {
		return PlaybookRunSnapshot{}, errors.New("root issue must be assigned to this squad")
	}
	if existing, err := s.Queries.GetActiveWorkflowRunForRoot(ctx, db.GetActiveWorkflowRunForRootParams{SquadID: squad.ID, RootIssueID: rootIssueID}); err == nil {
		return s.snapshot(ctx, existing)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return PlaybookRunSnapshot{}, fmt.Errorf("check active run: %w", err)
	}
	definitionRow, err := s.Queries.GetWorkflowDefinition(ctx, squad.WorkflowDefinitionID)
	if err != nil {
		return PlaybookRunSnapshot{}, fmt.Errorf("load playbook: %w", err)
	}
	definition, err := DecodePlaybookDefinition(definitionRow.Definition)
	if err != nil {
		return PlaybookRunSnapshot{}, fmt.Errorf("stored playbook is invalid: %w", err)
	}
	if len(contextRaw) == 0 {
		contextRaw = []byte(`{}`)
	}
	var runContext map[string]any
	if err := json.Unmarshal(contextRaw, &runContext); err != nil {
		return PlaybookRunSnapshot{}, errors.New("run context must be a JSON object")
	}
	if runContext == nil {
		return PlaybookRunSnapshot{}, errors.New("run context must be a JSON object")
	}
	delete(runContext, "_playbook")
	contextRaw, err = json.Marshal(runContext)
	if err != nil {
		return PlaybookRunSnapshot{}, errors.New("run context could not be normalized")
	}

	run, err := s.Queries.CreateWorkflowRun(ctx, db.CreateWorkflowRunParams{
		WorkflowDefinitionID:      definitionRow.ID,
		WorkflowDefinitionVersion: definitionRow.Version,
		DefinitionSnapshot:        definitionRow.Definition,
		WorkspaceID:               squad.WorkspaceID,
		SquadID:                   squad.ID,
		RootIssueID:               rootIssueID,
		Context:                   contextRaw,
		CreatedBy:                 createdBy,
	})
	if err != nil {
		if existing, lookupErr := s.Queries.GetActiveWorkflowRunForRoot(ctx, db.GetActiveWorkflowRunForRootParams{SquadID: squad.ID, RootIssueID: rootIssueID}); lookupErr == nil {
			return s.snapshot(ctx, existing)
		}
		return PlaybookRunSnapshot{}, fmt.Errorf("create playbook run: %w", err)
	}
	for _, step := range definition.Steps {
		agentID, _ := util.ParseUUID(step.AgentID)
		if _, err := s.Queries.CreateWorkflowNodeRun(ctx, db.CreateWorkflowNodeRunParams{WorkflowRunID: run.ID, StepKey: step.Key, AgentID: agentID}); err != nil {
			return PlaybookRunSnapshot{}, fmt.Errorf("create step %q: %w", step.Key, err)
		}
	}
	if err := s.advance(ctx, run, definition); err != nil {
		return PlaybookRunSnapshot{}, err
	}
	s.projectRootIssueStatus(ctx, run, "in_progress")
	return s.snapshot(ctx, run)
}

func (s *PlaybookService) DispatchStep(ctx context.Context, params DispatchPlaybookStepParams) (PlaybookRunSnapshot, error) {
	run, err := s.Queries.GetWorkflowRun(ctx, params.RunID)
	if err != nil {
		return PlaybookRunSnapshot{}, errors.New("playbook run not found")
	}
	definition, err := DecodePlaybookDefinition(run.DefinitionSnapshot)
	if err != nil {
		return PlaybookRunSnapshot{}, err
	}
	step, ok := findPlaybookStep(definition, params.StepKey)
	if !ok {
		return PlaybookRunSnapshot{}, fmt.Errorf("unknown step %q", params.StepKey)
	}
	node, err := s.Queries.GetWorkflowNodeRunByKey(ctx, db.GetWorkflowNodeRunByKeyParams{WorkflowRunID: run.ID, StepKey: step.Key})
	if err != nil {
		return PlaybookRunSnapshot{}, fmt.Errorf("load step: %w", err)
	}
	if node.Status != "pending" && node.Status != "needs_attention" {
		return s.snapshot(ctx, run)
	}
	agentID := node.AgentID
	if params.AgentID.Valid {
		if err := s.validateDispatchAgent(ctx, run, params.AgentID); err != nil {
			return PlaybookRunSnapshot{}, err
		}
		agentID = params.AgentID
	}
	maxAttempts := step.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = defaultMaxAttempts
	}
	if int(node.Attempt) >= maxAttempts {
		return PlaybookRunSnapshot{}, fmt.Errorf("step %q exhausted its retry budget", step.Key)
	}
	input := params.InputOverride
	if len(input) == 0 {
		input, err = s.materializeInput(run, step, mustListNodes(ctx, s.Queries, run.ID))
		if err != nil {
			return PlaybookRunSnapshot{}, err
		}
	} else {
		var object map[string]any
		if err := json.Unmarshal(input, &object); err != nil {
			return PlaybookRunSnapshot{}, errors.New("input override must be a JSON object")
		}
	}
	if node.Status == "pending" {
		node, err = s.Queries.MarkWorkflowNodeReady(ctx, db.MarkWorkflowNodeReadyParams{ID: node.ID, InputSnapshot: input})
	} else {
		node, err = s.Queries.ResetWorkflowNodeForRetry(ctx, db.ResetWorkflowNodeForRetryParams{ID: node.ID, AgentID: agentID, InputSnapshot: input})
	}
	if err != nil {
		return PlaybookRunSnapshot{}, fmt.Errorf("prepare step: %w", err)
	}
	if err := s.dispatchReadyNode(ctx, run, step, node, agentID); err != nil {
		return PlaybookRunSnapshot{}, err
	}
	if run.Status == "needs_attention" {
		if _, err := s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: run.ID, Status: "running"}); err != nil {
			return PlaybookRunSnapshot{}, fmt.Errorf("resume playbook run: %w", err)
		}
	}
	return s.snapshot(ctx, run)
}

func (s *PlaybookService) AcceptOutput(ctx context.Context, taskID pgtype.UUID, output []byte) (db.WorkflowNodeRun, error) {
	node, err := s.Queries.GetWorkflowNodeRunByTask(ctx, taskID)
	if err != nil {
		return db.WorkflowNodeRun{}, errors.New("task is not a playbook step")
	}
	task, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil || task.Status != "running" || task.AgentID != node.AgentID {
		return db.WorkflowNodeRun{}, errors.New("only the running step task can submit output")
	}
	run, err := s.Queries.GetWorkflowRun(ctx, node.WorkflowRunID)
	if err != nil {
		return db.WorkflowNodeRun{}, fmt.Errorf("load run: %w", err)
	}
	definition, err := DecodePlaybookDefinition(run.DefinitionSnapshot)
	if err != nil {
		return db.WorkflowNodeRun{}, err
	}
	step, ok := findPlaybookStep(definition, node.StepKey)
	if !ok {
		return db.WorkflowNodeRun{}, errors.New("playbook no longer contains this step")
	}
	var value any
	if err := json.Unmarshal(output, &value); err != nil {
		return db.WorkflowNodeRun{}, errors.New("output must be valid JSON")
	}
	if err := validateOutputValue(value, step.OutputSchema, "output"); err != nil {
		return db.WorkflowNodeRun{}, err
	}
	updated, err := s.Queries.AcceptWorkflowNodeOutput(ctx, db.AcceptWorkflowNodeOutputParams{
		ID:                  node.ID,
		Output:              output,
		AcceptedTaskID:      taskID,
		OutputSchemaVersion: run.WorkflowDefinitionVersion,
	})
	if err != nil {
		return db.WorkflowNodeRun{}, fmt.Errorf("accept output: %w", err)
	}
	return updated, nil
}

func (s *PlaybookService) HandleTaskCompleted(ctx context.Context, taskID pgtype.UUID) error {
	node, err := s.Queries.GetWorkflowNodeRunByTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load workflow step for task: %w", err)
	}
	if node.Status == "succeeded" || node.Status == "skipped" {
		return nil
	}
	run, err := s.Queries.GetWorkflowRun(ctx, node.WorkflowRunID)
	if err != nil {
		return fmt.Errorf("load workflow run: %w", err)
	}
	if !node.AcceptedTaskID.Valid || node.AcceptedTaskID != taskID || node.Output == nil {
		if _, markErr := s.Queries.MarkWorkflowNodeNeedsAttention(ctx, db.MarkWorkflowNodeNeedsAttentionParams{ID: node.ID, Error: "task completed without accepted structured output"}); markErr != nil {
			return markErr
		}
		_, _ = s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: run.ID, Status: "needs_attention"})
		return nil
	}
	completed, err := s.Queries.MarkWorkflowNodeSucceeded(ctx, node.ID)
	if err != nil {
		return fmt.Errorf("complete workflow step: %w", err)
	}
	definition, err := DecodePlaybookDefinition(run.DefinitionSnapshot)
	if err != nil {
		return err
	}
	if definition.Start != "" {
		return s.advanceStateMachine(ctx, run, definition, completed)
	}
	return s.advance(ctx, run, definition)
}

func (s *PlaybookService) HandleTaskFailed(ctx context.Context, taskID pgtype.UUID, reason string) error {
	node, err := s.Queries.GetWorkflowNodeRunByTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	failedTask, err := s.Queries.GetAgentTask(ctx, taskID)
	if err == nil && failedTask.IssueID.Valid {
		latest, latestErr := s.Queries.GetLatestAgentTaskForIssue(ctx, db.GetLatestAgentTaskForIssueParams{IssueID: failedTask.IssueID, AgentID: failedTask.AgentID})
		if latestErr == nil && latest.ID != taskID && latest.ParentTaskID == taskID && latest.Status == "queued" {
			_, rebindErr := s.Queries.RebindWorkflowNodeRetryTask(ctx, db.RebindWorkflowNodeRetryTaskParams{ID: node.ID, TaskID: latest.ID, TaskID_2: taskID})
			return rebindErr
		}
	}
	if _, err := s.Queries.MarkWorkflowNodeNeedsAttention(ctx, db.MarkWorkflowNodeNeedsAttentionParams{ID: node.ID, Error: reason}); err != nil {
		return err
	}
	_, err = s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: node.WorkflowRunID, Status: "needs_attention"})
	return err
}

func (s *PlaybookService) advance(ctx context.Context, run db.WorkflowRun, definition PlaybookDefinition) error {
	if definition.Start != "" {
		return s.activateStateStep(ctx, run, definition, definition.Start)
	}
	for iteration := 0; iteration < len(definition.Steps)+1; iteration++ {
		nodes, err := s.Queries.ListWorkflowNodeRuns(ctx, run.ID)
		if err != nil {
			return fmt.Errorf("list workflow steps: %w", err)
		}
		byKey := make(map[string]db.WorkflowNodeRun, len(nodes))
		for _, node := range nodes {
			byKey[node.StepKey] = node
		}
		progressed := false
		for _, step := range definition.Steps {
			node := byKey[step.Key]
			if node.Status != "pending" || !dependenciesTerminal(step, byKey) {
				continue
			}
			matches, err := conditionMatches(step, byKey)
			if err != nil {
				_, _ = s.Queries.MarkWorkflowNodeNeedsAttention(ctx, db.MarkWorkflowNodeNeedsAttentionParams{ID: node.ID, Error: err.Error()})
				_, _ = s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: run.ID, Status: "needs_attention"})
				return nil
			}
			if !matches {
				if _, err := s.Queries.MarkWorkflowNodeSkipped(ctx, node.ID); err != nil {
					return err
				}
				progressed = true
				continue
			}
			input, err := s.materializeInput(run, step, nodes)
			if err != nil {
				_, _ = s.Queries.MarkWorkflowNodeNeedsAttention(ctx, db.MarkWorkflowNodeNeedsAttentionParams{ID: node.ID, Error: err.Error()})
				_, _ = s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: run.ID, Status: "needs_attention"})
				return nil
			}
			ready, err := s.Queries.MarkWorkflowNodeReady(ctx, db.MarkWorkflowNodeReadyParams{ID: node.ID, InputSnapshot: input})
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				return err
			}
			if err := s.dispatchReadyNode(ctx, run, step, ready, ready.AgentID); err != nil {
				_, _ = s.Queries.MarkWorkflowNodeNeedsAttention(ctx, db.MarkWorkflowNodeNeedsAttentionParams{ID: ready.ID, Error: err.Error()})
				_, _ = s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: run.ID, Status: "needs_attention"})
				return nil
			}
			progressed = true
		}
		if !progressed {
			break
		}
	}
	nodes, err := s.Queries.ListWorkflowNodeRuns(ctx, run.ID)
	if err != nil {
		return err
	}
	allDone := len(nodes) > 0
	for _, node := range nodes {
		if node.Status == "needs_attention" || node.Status == "failed" {
			_, _ = s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: run.ID, Status: "needs_attention"})
			return nil
		}
		if node.Status != "succeeded" && node.Status != "skipped" {
			allDone = false
		}
	}
	if allDone {
		_, err = s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: run.ID, Status: "succeeded"})
		if err == nil {
			s.projectRootIssueStatus(ctx, run, "in_review")
		}
	}
	return err
}

type playbookRuntimeState struct {
	TransitionCount int                       `json:"transition_count"`
	Trace           []playbookTransitionTrace `json:"trace,omitempty"`
}

type playbookTransitionTrace struct {
	Sequence int             `json:"sequence"`
	From     string          `json:"from"`
	To       string          `json:"to,omitempty"`
	End      bool            `json:"end,omitempty"`
	Output   json.RawMessage `json:"output"`
}

func (s *PlaybookService) advanceStateMachine(ctx context.Context, run db.WorkflowRun, definition PlaybookDefinition, completed db.WorkflowNodeRun) error {
	transition, matched, err := selectPlaybookTransition(definition, completed)
	if err != nil {
		return s.stopStateMachine(ctx, run, completed, err.Error())
	}
	if !matched {
		return s.stopStateMachine(ctx, run, completed, fmt.Sprintf("step %q output matched no transition", completed.StepKey))
	}

	contextValue, state, err := decodePlaybookRuntimeState(run.Context)
	if err != nil {
		return s.stopStateMachine(ctx, run, completed, err.Error())
	}
	maxTransitions := definition.MaxTransitions
	if maxTransitions == 0 {
		maxTransitions = defaultMaxTransitions
	}
	if state.TransitionCount >= maxTransitions {
		return s.stopStateMachine(ctx, run, completed, fmt.Sprintf("run exhausted max_transitions (%d)", maxTransitions))
	}
	state.TransitionCount++
	state.Trace = append(state.Trace, playbookTransitionTrace{
		Sequence: state.TransitionCount,
		From:     completed.StepKey,
		To:       transition.To,
		End:      transition.End,
		Output:   append(json.RawMessage(nil), completed.Output...),
	})
	contextValue["_playbook"] = state
	contextRaw, err := json.Marshal(contextValue)
	if err != nil {
		return s.stopStateMachine(ctx, run, completed, "could not persist transition trace")
	}
	updatedRun, err := s.Queries.UpdateWorkflowRunContext(ctx, db.UpdateWorkflowRunContextParams{ID: run.ID, Context: contextRaw})
	if err != nil {
		return fmt.Errorf("persist playbook transition: %w", err)
	}
	if transition.End {
		if _, err := s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: run.ID, Status: "succeeded"}); err != nil {
			return err
		}
		s.projectRootIssueStatus(ctx, updatedRun, "in_review")
		return nil
	}
	return s.activateStateStep(ctx, updatedRun, definition, transition.To)
}

func (s *PlaybookService) activateStateStep(ctx context.Context, run db.WorkflowRun, definition PlaybookDefinition, key string) error {
	step, ok := findPlaybookStep(definition, key)
	if !ok {
		return fmt.Errorf("unknown state-machine step %q", key)
	}
	nodes, err := s.Queries.ListWorkflowNodeRuns(ctx, run.ID)
	if err != nil {
		return err
	}
	node := findNodeRun(nodes, key)
	if !node.ID.Valid {
		return fmt.Errorf("state-machine step %q has no node run", key)
	}
	maxAttempts := step.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = defaultMaxAttempts
	}
	if int(node.Attempt) >= maxAttempts {
		return s.stopStateMachine(ctx, run, node, fmt.Sprintf("step %q exhausted max_attempts (%d)", key, maxAttempts))
	}
	input, err := s.materializeInput(run, step, nodes)
	if err != nil {
		return s.stopStateMachine(ctx, run, node, err.Error())
	}
	ready, err := s.Queries.ActivateWorkflowNode(ctx, db.ActivateWorkflowNodeParams{ID: node.ID, InputSnapshot: input})
	if err != nil {
		return fmt.Errorf("activate state-machine step %q: %w", key, err)
	}
	if err := s.dispatchReadyNode(ctx, run, step, ready, ready.AgentID); err != nil {
		return s.stopStateMachine(ctx, run, ready, err.Error())
	}
	return nil
}

func (s *PlaybookService) stopStateMachine(ctx context.Context, run db.WorkflowRun, node db.WorkflowNodeRun, reason string) error {
	if node.ID.Valid {
		_, _ = s.Queries.MarkWorkflowNodeNeedsAttention(ctx, db.MarkWorkflowNodeNeedsAttentionParams{ID: node.ID, Error: reason})
	}
	_, err := s.Queries.UpdateWorkflowRunStatus(ctx, db.UpdateWorkflowRunStatusParams{ID: run.ID, Status: "needs_attention"})
	return err
}

func selectPlaybookTransition(definition PlaybookDefinition, completed db.WorkflowNodeRun) (PlaybookTransition, bool, error) {
	step, ok := findPlaybookStep(definition, completed.StepKey)
	if !ok {
		return PlaybookTransition{}, false, fmt.Errorf("unknown completed step %q", completed.StepKey)
	}
	var output any
	if err := json.Unmarshal(completed.Output, &output); err != nil {
		return PlaybookTransition{}, false, fmt.Errorf("step %q has invalid transition output", completed.StepKey)
	}
	for _, transition := range step.Transitions {
		if transition.When == nil {
			return transition, true, nil
		}
		actual, found := lookupJSONPath(output, strings.Split(transition.When.Field, "."))
		if !found {
			return PlaybookTransition{}, false, fmt.Errorf("step %q transition field %q is missing", completed.StepKey, transition.When.Field)
		}
		var expected any
		_ = json.Unmarshal(transition.When.Equals, &expected)
		if valuesEqual(actual, expected) {
			return transition, true, nil
		}
	}
	return PlaybookTransition{}, false, nil
}

func decodePlaybookRuntimeState(raw []byte) (map[string]any, playbookRuntimeState, error) {
	var contextValue map[string]any
	if err := json.Unmarshal(raw, &contextValue); err != nil || contextValue == nil {
		return nil, playbookRuntimeState{}, errors.New("stored run context is invalid")
	}
	var state playbookRuntimeState
	if runtimeValue, ok := contextValue["_playbook"]; ok {
		runtimeRaw, _ := json.Marshal(runtimeValue)
		if err := json.Unmarshal(runtimeRaw, &state); err != nil {
			return nil, state, errors.New("stored playbook runtime state is invalid")
		}
	}
	return contextValue, state, nil
}

func (s *PlaybookService) projectRootIssueStatus(ctx context.Context, run db.WorkflowRun, target string) {
	issue, err := s.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          run.RootIssueID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		slog.Warn("playbook: load root issue for status projection failed", "run_id", run.ID, "error", err)
		return
	}
	if issue.Status == "done" || issue.Status == "cancelled" || issue.Status == target {
		return
	}
	if target == "in_progress" && issue.Status != "backlog" && issue.Status != "todo" {
		return
	}
	if target == "in_review" && issue.Status != "backlog" && issue.Status != "todo" && issue.Status != "in_progress" {
		return
	}
	updated, err := s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      target,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		slog.Warn("playbook: project root issue status failed", "run_id", run.ID, "status", target, "error", err)
		return
	}
	if s.IssueService == nil || s.IssueService.Bus == nil {
		return
	}
	prefix := ""
	if workspace, workspaceErr := s.Queries.GetWorkspace(ctx, issue.WorkspaceID); workspaceErr == nil {
		prefix = workspace.IssuePrefix
	}
	s.IssueService.Bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: util.UUIDToString(issue.WorkspaceID),
		ActorType:   "system",
		ActorID:     "",
		Payload: map[string]any{
			"issue":          issueToMap(updated, prefix),
			"status_changed": true,
			"prev_status":    issue.Status,
			"source":         "playbook_run",
		},
	})
}

func (s *PlaybookService) dispatchReadyNode(ctx context.Context, run db.WorkflowRun, step PlaybookStep, node db.WorkflowNodeRun, agentID pgtype.UUID) error {
	if err := s.validateDispatchAgent(ctx, run, agentID); err != nil {
		return err
	}
	if node.IssueID.Valid && node.TaskID.Valid {
		issue, err := s.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
			ID: node.IssueID, WorkspaceID: run.WorkspaceID,
		})
		if err != nil {
			return fmt.Errorf("load issue for step %q retry: %w", step.Key, err)
		}
		if issue.AssigneeType.String != "agent" || issue.AssigneeID != agentID {
			return fmt.Errorf("step %q retry with a different agent is not supported yet", step.Key)
		}
		if issue.Status != "todo" {
			issue, err = s.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{
				ID: issue.ID, Status: "todo", WorkspaceID: issue.WorkspaceID,
			})
			if err != nil {
				return fmt.Errorf("reset issue for step %q retry: %w", step.Key, err)
			}
		}
		if _, err := s.Queries.UpdateWorkflowStepIssueDescription(ctx, db.UpdateWorkflowStepIssueDescriptionParams{
			ID: issue.ID, WorkspaceID: issue.WorkspaceID, Description: pgtype.Text{String: playbookStepDescription(step, node.InputSnapshot), Valid: true},
		}); err != nil {
			return fmt.Errorf("refresh issue handoff for step %q: %w", step.Key, err)
		}
		task, err := s.IssueService.TaskService.RerunIssue(ctx, issue.ID, node.TaskID, pgtype.UUID{})
		if err != nil {
			return fmt.Errorf("enqueue retry for step %q: %w", step.Key, err)
		}
		if _, err := s.Queries.MarkWorkflowNodeRunning(ctx, db.MarkWorkflowNodeRunningParams{
			ID: node.ID, AgentID: agentID, IssueID: issue.ID, TaskID: task.ID,
		}); err != nil {
			return fmt.Errorf("start retry for step %q: %w", step.Key, err)
		}
		return nil
	}
	description := playbookStepDescription(step, node.InputSnapshot)
	result, err := s.IssueService.Create(ctx, IssueCreateParams{
		WorkspaceID:    run.WorkspaceID,
		Title:          step.Title,
		Description:    pgtype.Text{String: description, Valid: true},
		Status:         "todo",
		Priority:       "none",
		AssigneeType:   pgtype.Text{String: "agent", Valid: true},
		AssigneeID:     agentID,
		CreatorType:    "member",
		CreatorID:      run.CreatedBy,
		ParentIssueID:  run.RootIssueID,
		AllowDuplicate: true,
	}, IssueCreateOpts{ActorID: util.UUIDToString(run.CreatedBy), Platform: "workflow"})
	if err != nil {
		return fmt.Errorf("create issue for step %q: %w", step.Key, err)
	}
	task, err := s.Queries.GetLatestAgentTaskForIssue(ctx, db.GetLatestAgentTaskForIssueParams{IssueID: result.Issue.ID, AgentID: agentID})
	if err != nil {
		return fmt.Errorf("step %q agent is not ready for dispatch", step.Key)
	}
	if _, err := s.Queries.MarkWorkflowNodeRunning(ctx, db.MarkWorkflowNodeRunningParams{ID: node.ID, AgentID: agentID, IssueID: result.Issue.ID, TaskID: task.ID}); err != nil {
		return fmt.Errorf("start step %q: %w", step.Key, err)
	}
	return nil
}

func playbookStepDescription(step PlaybookStep, input []byte) string {
	prettyInput := string(input)
	var formatted bytes.Buffer
	if json.Indent(&formatted, input, "", "  ") == nil {
		prettyInput = formatted.String()
	}
	return strings.TrimSpace(step.Instructions) + "\n\n## Handoff input\n\n```json\n" + prettyInput + "\n```\n\nSubmit the structured result before finishing:\n\n```bash\nmultica task output set --task \"$MULTICA_TASK_ID\" --json-file result.json\n```"
}

func (s *PlaybookService) validateDispatchAgent(ctx context.Context, run db.WorkflowRun, agentID pgtype.UUID) error {
	agent, err := s.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: run.WorkspaceID})
	if err != nil || agent.ArchivedAt.Valid {
		return errors.New("dispatch agent is unavailable")
	}
	isMember, err := s.Queries.IsSquadMember(ctx, db.IsSquadMemberParams{SquadID: run.SquadID, MemberType: "agent", MemberID: agentID})
	if err != nil || !isMember {
		return errors.New("dispatch agent must be a member of the run squad")
	}
	return nil
}

func (s *PlaybookService) materializeInput(run db.WorkflowRun, step PlaybookStep, nodes []db.WorkflowNodeRun) ([]byte, error) {
	var contextValue map[string]any
	if err := json.Unmarshal(run.Context, &contextValue); err != nil {
		return nil, errors.New("stored run context is invalid")
	}
	delete(contextValue, "_playbook")
	byKey := make(map[string]db.WorkflowNodeRun, len(nodes))
	upstream := make(map[string]any, len(nodes))
	for _, node := range nodes {
		byKey[node.StepKey] = node
	}
	sources := step.DependsOn
	if len(step.Transitions) > 0 {
		sources = make([]string, 0, len(nodes))
		for _, node := range nodes {
			if node.Output != nil {
				sources = append(sources, node.StepKey)
			}
		}
	}
	for _, dependency := range sources {
		node := byKey[dependency]
		if node.Status == "skipped" {
			continue
		}
		var value any
		if err := json.Unmarshal(node.Output, &value); err != nil {
			return nil, fmt.Errorf("dependency %q has invalid output", dependency)
		}
		upstream[dependency] = value
	}
	mapped := make(map[string]any, len(step.Input))
	keys := make([]string, 0, len(step.Input))
	for key := range step.Input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		binding := step.Input[key]
		if len(binding.Value) > 0 {
			var value any
			_ = json.Unmarshal(binding.Value, &value)
			mapped[key] = value
			continue
		}
		parts := strings.Split(binding.From, ".")
		var root any
		if parts[0] == "context" {
			root = contextValue
		} else {
			root = upstream[parts[0]]
		}
		value, ok := lookupJSONPath(root, parts[1:])
		if !ok && len(binding.Default) > 0 {
			_ = json.Unmarshal(binding.Default, &value)
			ok = true
		}
		if !ok {
			return nil, fmt.Errorf("input %q could not read %q", key, binding.From)
		}
		mapped[key] = value
	}
	return json.Marshal(map[string]any{"context": contextValue, "upstream": upstream, "input": mapped})
}

func dependenciesTerminal(step PlaybookStep, nodes map[string]db.WorkflowNodeRun) bool {
	for _, dependency := range step.DependsOn {
		status := nodes[dependency].Status
		if status != "succeeded" && status != "skipped" {
			return false
		}
	}
	return true
}

func conditionMatches(step PlaybookStep, nodes map[string]db.WorkflowNodeRun) (bool, error) {
	if step.When == nil {
		return true, nil
	}
	node := nodes[step.When.Step]
	if node.Status == "skipped" {
		return false, nil
	}
	var output any
	if err := json.Unmarshal(node.Output, &output); err != nil {
		return false, fmt.Errorf("condition source %q has invalid output", step.When.Step)
	}
	actual, ok := lookupJSONPath(output, strings.Split(step.When.Field, "."))
	if !ok {
		return false, fmt.Errorf("condition field %q is missing", step.When.Field)
	}
	var expected any
	_ = json.Unmarshal(step.When.Equals, &expected)
	return valuesEqual(actual, expected), nil
}

func validateOutputValue(value any, schema ValueSchema, path string) error {
	if len(schema.Enum) > 0 {
		matched := false
		for _, raw := range schema.Enum {
			var candidate any
			_ = json.Unmarshal(raw, &candidate)
			if valuesEqual(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s must be one of the declared enum values", path)
		}
	}
	switch schema.Type {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		for _, required := range schema.Required {
			if _, ok := object[required]; !ok {
				return fmt.Errorf("%s.%s is required", path, required)
			}
		}
		for name, child := range object {
			property, known := schema.Properties[name]
			if !known {
				if schema.AdditionalProperties != nil && !*schema.AdditionalProperties {
					return fmt.Errorf("%s.%s is not allowed", path, name)
				}
				continue
			}
			if err := validateOutputValue(child, property, path+"."+name); err != nil {
				return err
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be an array", path)
		}
		for i, item := range array {
			if err := validateOutputValue(item, *schema.Items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("%s must be a number", path)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || number != float64(int64(number)) {
			return fmt.Errorf("%s must be an integer", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "null":
		if value != nil {
			return fmt.Errorf("%s must be null", path)
		}
	}
	return nil
}

func lookupJSONPath(value any, path []string) (any, bool) {
	current := value
	for _, segment := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func valuesEqual(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func findPlaybookStep(definition PlaybookDefinition, key string) (PlaybookStep, bool) {
	for _, step := range definition.Steps {
		if step.Key == key {
			return step, true
		}
	}
	return PlaybookStep{}, false
}

func findNodeRun(nodes []db.WorkflowNodeRun, key string) db.WorkflowNodeRun {
	for _, node := range nodes {
		if node.StepKey == key {
			return node
		}
	}
	return db.WorkflowNodeRun{}
}

func mustListNodes(ctx context.Context, queries *db.Queries, runID pgtype.UUID) []db.WorkflowNodeRun {
	nodes, _ := queries.ListWorkflowNodeRuns(ctx, runID)
	return nodes
}

func (s *PlaybookService) snapshot(ctx context.Context, run db.WorkflowRun) (PlaybookRunSnapshot, error) {
	latest, err := s.Queries.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		return PlaybookRunSnapshot{}, err
	}
	nodes, err := s.Queries.ListWorkflowNodeRuns(ctx, run.ID)
	if err != nil {
		return PlaybookRunSnapshot{}, err
	}
	return PlaybookRunSnapshot{Run: latest, Nodes: nodes}, nil
}
