package service

import (
	"reflect"
	"testing"
	"time"
)

// Reducer tests exhaustively walk the transition table of AdvanceWorkflow.
// The reducer is the engine's contract: agents and humans do the work, the
// reducer decides every transition, so each decision kind gets pinned here.

func advTestDef(t *testing.T) *WorkflowDef {
	t.Helper()
	def, err := ParseWorkflowDef(validWorkflowYAML)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return def
}

func advTestState(frontier []string, steps map[string]*WorkflowStepState) *WorkflowRunState {
	if steps == nil {
		steps = map[string]*WorkflowStepState{}
	}
	return &WorkflowRunState{
		Version:  1,
		Workflow: "release-flow",
		Status:   WorkflowRunRunning,
		Frontier: frontier,
		Steps:    steps,
	}
}

func attempted(keys ...string) map[string]*WorkflowStepState {
	out := map[string]*WorkflowStepState{}
	for i, k := range keys {
		out[k] = &WorkflowStepState{Attempts: []WorkflowStepAttempt{{IssueID: "id", N: 1, CreatedAt: time.Now().Add(-time.Duration(i) * time.Minute)}}}
	}
	return out
}

var now = time.Now().UTC()

func TestAdvanceWaitsWhileFrontierOpen(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"regression", "changelog"}, attempted("regression", "changelog"))
	snaps := map[string]WorkflowStepSnapshot{
		"regression": {IssueStatus: "done", CreatedAt: now, Outputs: map[string]string{"result": "pass"}},
		"changelog":  {IssueStatus: "in_progress", CreatedAt: now},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideNone {
		t.Fatalf("expected none while a sibling is open, got %+v", d)
	}
}

func TestAdvanceStepTimeout(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"regression", "changelog"}, attempted("regression", "changelog"))
	snaps := map[string]WorkflowStepSnapshot{
		"regression": {IssueStatus: "in_progress", CreatedAt: now.Add(-5 * time.Hour)}, // step timeout 4h
		"changelog":  {IssueStatus: "in_progress", CreatedAt: now},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideAttention {
		t.Fatalf("expected attention on timeout, got %+v", d)
	}
}

func TestAdvanceRunDeadline(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"regression"}, attempted("regression"))
	past := now.Add(-time.Hour)
	state.Deadline = &past
	d := AdvanceWorkflow(def, state, map[string]WorkflowStepSnapshot{
		"regression": {IssueStatus: "in_progress", CreatedAt: now},
	}, now)
	if d.Kind != WorkflowDecideAttention {
		t.Fatalf("expected attention on run deadline, got %+v", d)
	}
}

func TestAdvanceRoutesOnOutput(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"regression", "changelog"}, attempted("regression", "changelog"))
	snaps := map[string]WorkflowStepSnapshot{
		"regression": {IssueStatus: "done", CreatedAt: now, Outputs: map[string]string{"result": "pass"}},
		"changelog":  {IssueStatus: "done", CreatedAt: now},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideActivate || !reflect.DeepEqual(d.Activate, []string{"gate"}) {
		t.Fatalf("expected activate [gate], got %+v", d)
	}
}

func TestAdvanceEnumDriftTakesDefault(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"regression", "changelog"}, attempted("regression", "changelog"))
	snaps := map[string]WorkflowStepSnapshot{
		// Agent wrote garbage (or nothing): default branch, never a crash.
		"regression": {IssueStatus: "done", CreatedAt: now, Outputs: map[string]string{"result": "maybe"}},
		"changelog":  {IssueStatus: "done", CreatedAt: now},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideAttention {
		t.Fatalf("expected default branch needs_attention, got %+v", d)
	}
}

func TestAdvanceApprovalApprovedFallsThrough(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"gate"}, attempted("gate"))
	snaps := map[string]WorkflowStepSnapshot{
		"gate": {IssueStatus: "done", CreatedAt: now},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideActivate || !reflect.DeepEqual(d.Activate, []string{"ship"}) {
		t.Fatalf("expected activate [ship], got %+v", d)
	}
}

func TestAdvanceApprovalRejectedLoopsBack(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"gate"}, attempted("gate"))
	snaps := map[string]WorkflowStepSnapshot{
		"gate": {IssueStatus: "cancelled", CreatedAt: now, RejectReason: "coverage too low"},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideActivate {
		t.Fatalf("expected activate on rejection, got %+v", d)
	}
	// back_to regression activates its whole stage group (parallel siblings
	// rerun together).
	if !reflect.DeepEqual(d.Activate, []string{"regression", "changelog"}) {
		t.Fatalf("expected stage-1 group, got %v", d.Activate)
	}
	if d.LoopStep != "gate" || d.RejectReason != "coverage too low" {
		t.Fatalf("loop bookkeeping wrong: %+v", d)
	}
}

func TestAdvanceRejectionLoopBudgetExhausted(t *testing.T) {
	def := advTestDef(t)
	steps := attempted("gate")
	steps["gate"].LoopsUsed = 2 // max_loops: 2
	state := advTestState([]string{"gate"}, steps)
	snaps := map[string]WorkflowStepSnapshot{
		"gate": {IssueStatus: "cancelled", CreatedAt: now},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideAttention {
		t.Fatalf("expected attention when loop budget exhausted, got %+v", d)
	}
}

func TestAdvanceFailedStepRetries(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"regression", "changelog"}, attempted("regression", "changelog"))
	snaps := map[string]WorkflowStepSnapshot{
		"regression": {IssueStatus: "cancelled", CreatedAt: now}, // on_fail retry: 1, attempt 1
		"changelog":  {IssueStatus: "done", CreatedAt: now},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideActivate || !reflect.DeepEqual(d.Activate, []string{"regression"}) {
		t.Fatalf("expected retry of regression only, got %+v", d)
	}
}

func TestAdvanceFailedStepBudgetExhausted(t *testing.T) {
	def := advTestDef(t)
	steps := attempted("regression", "changelog")
	steps["regression"].Attempts = append(steps["regression"].Attempts, WorkflowStepAttempt{IssueID: "id2", N: 2, CreatedAt: now})
	state := advTestState([]string{"regression", "changelog"}, steps)
	snaps := map[string]WorkflowStepSnapshot{
		"regression": {IssueStatus: "cancelled", CreatedAt: now}, // attempt 2, retry budget 1 spent
		"changelog":  {IssueStatus: "done", CreatedAt: now},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideAttention {
		t.Fatalf("expected attention when retry budget spent, got %+v", d)
	}
}

func TestAdvanceFinalStageCompletes(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"ship"}, attempted("ship"))
	snaps := map[string]WorkflowStepSnapshot{
		"ship": {IssueStatus: "done", CreatedAt: now},
	}
	d := AdvanceWorkflow(def, state, snaps, now)
	if d.Kind != WorkflowDecideComplete {
		t.Fatalf("expected complete after final stage, got %+v", d)
	}
}

func TestAdvanceMissingFrontierSnapshotEscalates(t *testing.T) {
	def := advTestDef(t)
	state := advTestState([]string{"regression"}, attempted("regression"))
	d := AdvanceWorkflow(def, state, map[string]WorkflowStepSnapshot{}, now)
	if d.Kind != WorkflowDecideAttention {
		t.Fatalf("expected attention on missing snapshot, got %+v", d)
	}
}

func TestAdvanceNonRunningIsInert(t *testing.T) {
	def := advTestDef(t)
	for _, status := range []string{WorkflowRunPaused, WorkflowRunNeedsAttention, WorkflowRunEjected, WorkflowRunDone, WorkflowRunCancelled} {
		state := advTestState([]string{"regression"}, attempted("regression"))
		state.Status = status
		d := AdvanceWorkflow(def, state, map[string]WorkflowStepSnapshot{}, now)
		if d.Kind != WorkflowDecideNone {
			t.Fatalf("status %s: expected none, got %+v", status, d)
		}
	}
}
