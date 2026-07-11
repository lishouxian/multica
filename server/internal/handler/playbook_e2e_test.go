package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestPlaybookSquadStructuredHandoffE2E exercises the durable control path with
// real PostgreSQL rows and the production IssueService/TaskService:
//
//	triage -> implement --\
//	       -> security ----+-> review
//	       -> close (skipped)
//
// It proves schema-accepted output is the only handoff source, a false branch
// is skipped, review waits for both active dependencies, and the downstream
// input snapshot contains the accepted upstream values.
func TestPlaybookSquadStructuredHandoffE2E(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	agents := make(map[string]string)
	for _, name := range []string{"leader", "triage", "implement", "security", "review"} {
		agents[name] = createHandlerTestAgent(t, "playbook-e2e-"+name, nil)
	}

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, $2, '', $3, $4)
		RETURNING id
	`, testWorkspaceID, "Playbook E2E Squad", agents["leader"], testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	for role, agentID := range agents {
		if _, err := testPool.Exec(ctx, `INSERT INTO squad_member (squad_id, member_type, member_id, role) VALUES ($1, 'agent', $2, $3)`, squadID, agentID, role); err != nil {
			t.Fatalf("add %s to squad: %v", role, err)
		}
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id = $1 AND title LIKE 'Playbook E2E%'`, testWorkspaceID)
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	squad, err := testHandler.Queries.GetSquad(ctx, util.MustParseUUID(squadID))
	if err != nil {
		t.Fatalf("load squad: %v", err)
	}
	playbook := fmt.Sprintf(`{
	  "version": 1,
	  "steps": [
	    {
	      "key": "triage", "title": "Playbook E2E triage", "agent_id": %q,
	      "instructions": "Classify the request.",
	      "output_schema": {"type":"object","required":["outcome","summary"],"properties":{"outcome":{"type":"string","enum":["fix","close"]},"summary":{"type":"string"}},"additional_properties":false}
	    },
	    {
	      "key": "implement", "title": "Playbook E2E implement", "agent_id": %q,
	      "depends_on": ["triage"], "when":{"step":"triage","field":"outcome","equals":"fix"},
	      "input":{"problem":{"from":"triage.summary"}},
	      "output_schema":{"type":"object","required":["commit"],"properties":{"commit":{"type":"string"}},"additional_properties":false}
	    },
	    {
	      "key": "security", "title": "Playbook E2E security", "agent_id": %q,
	      "depends_on": ["triage"], "when":{"step":"triage","field":"outcome","equals":"fix"},
	      "input":{"problem":{"from":"triage.summary"}},
	      "output_schema":{"type":"object","required":["approved"],"properties":{"approved":{"type":"boolean"}},"additional_properties":false}
	    },
	    {
	      "key": "close", "title": "Playbook E2E close", "agent_id": %q,
	      "depends_on": ["triage"], "when":{"step":"triage","field":"outcome","equals":"close"},
	      "output_schema":{"type":"object","properties":{},"additional_properties":false}
	    },
	    {
	      "key": "review", "title": "Playbook E2E review", "agent_id": %q,
	      "depends_on": ["implement","security","close"],
	      "input":{"commit":{"from":"implement.commit"},"approved":{"from":"security.approved"}},
	      "output_schema":{"type":"object","required":["verdict"],"properties":{"verdict":{"type":"string","enum":["pass","fail"]}},"additional_properties":false}
	    }
	  ]
	}`, agents["triage"], agents["implement"], agents["security"], agents["review"], agents["review"])
	if _, err := testHandler.PlaybookService.SaveDefinition(ctx, squad, []byte(playbook)); err != nil {
		t.Fatalf("save playbook: %v", err)
	}
	squad, _ = testHandler.Queries.GetSquad(ctx, util.MustParseUUID(squadID))
	briefing := buildSquadLeaderBriefing(ctx, testHandler.Queries, squad)
	for _, want := range []string{"## Squad Playbook Operating Protocol", "multica squad run start " + squadID, "`triage`", "multica squad dispatch"} {
		if !strings.Contains(briefing, want) {
			t.Fatalf("playbook leader briefing missing %q:\n%s", want, briefing)
		}
	}

	rootResult, err := testHandler.IssueService.Create(ctx, service.IssueCreateParams{
		WorkspaceID:  util.MustParseUUID(testWorkspaceID),
		Title:        "Playbook E2E root",
		Status:       "backlog",
		Priority:     "none",
		AssigneeType: pgtype.Text{String: "squad", Valid: true},
		AssigneeID:   squad.ID,
		CreatorType:  "member",
		CreatorID:    util.MustParseUUID(testUserID),
	}, service.IssueCreateOpts{Platform: "test"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	snapshot, err := testHandler.PlaybookService.StartRun(ctx, squad, rootResult.Issue.ID, util.MustParseUUID(testUserID), []byte(`{"request":"fix login"}`))
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	assertNodeStatus(t, snapshot.Nodes, "triage", "running")
	projectedRoot, err := testHandler.Queries.GetIssue(ctx, rootResult.Issue.ID)
	if err != nil {
		t.Fatalf("reload projected root: %v", err)
	}
	if projectedRoot.Status != "in_progress" {
		t.Fatalf("root status after run start = %s, want in_progress", projectedRoot.Status)
	}

	// Editing the squad playbook after a run starts must not change that run's
	// contracts or graph. The new version rejects "fix"; the active run still
	// accepts it from its immutable definition snapshot.
	nextPlaybook := strings.Replace(playbook, `"enum":["fix","close"]`, `"enum":["close"]`, 1)
	if _, err := testHandler.PlaybookService.SaveDefinition(ctx, squad, []byte(nextPlaybook)); err != nil {
		t.Fatalf("save next playbook version: %v", err)
	}

	snapshot = completePlaybookNode(t, snapshot, "triage", `{"outcome":"fix","summary":"token expiry is ignored"}`)
	assertNodeStatus(t, snapshot.Nodes, "implement", "running")
	assertNodeStatus(t, snapshot.Nodes, "security", "running")
	assertNodeStatus(t, snapshot.Nodes, "close", "skipped")
	assertNodeStatus(t, snapshot.Nodes, "review", "pending")

	snapshot = completePlaybookNode(t, snapshot, "implement", `{"commit":"abc123"}`)
	assertNodeStatus(t, snapshot.Nodes, "review", "pending")
	snapshot = completePlaybookNode(t, snapshot, "security", `{"approved":true}`)
	review := findNode(t, snapshot.Nodes, "review")
	if review.Status != "running" {
		t.Fatalf("review status = %s, want running", review.Status)
	}
	var input struct {
		Input map[string]any `json:"input"`
	}
	if err := json.Unmarshal(review.InputSnapshot, &input); err != nil {
		t.Fatalf("decode review input: %v", err)
	}
	if input.Input["commit"] != "abc123" || input.Input["approved"] != true {
		t.Fatalf("review input = %#v, want accepted implement/security outputs", input.Input)
	}

	snapshot = completePlaybookNode(t, snapshot, "review", `{"verdict":"pass"}`)
	if snapshot.Run.Status != "succeeded" {
		t.Fatalf("run status = %s, want succeeded", snapshot.Run.Status)
	}
	projectedRoot, err = testHandler.Queries.GetIssue(ctx, rootResult.Issue.ID)
	if err != nil {
		t.Fatalf("reload completed root: %v", err)
	}
	if projectedRoot.Status != "in_review" {
		t.Fatalf("root status after run success = %s, want in_review", projectedRoot.Status)
	}
	for label, issueID := range map[string]pgtype.UUID{
		"root": rootResult.Issue.ID,
		"step": findNode(t, snapshot.Nodes, "review").IssueID,
	} {
		projected, err := testHandler.Queries.GetLatestWorkflowRunForIssue(ctx, db.GetLatestWorkflowRunForIssueParams{
			WorkspaceID: util.MustParseUUID(testWorkspaceID),
			IssueID:     issueID,
		})
		if err != nil {
			t.Fatalf("load %s issue playbook projection: %v", label, err)
		}
		if projected.ID != snapshot.Run.ID {
			t.Fatalf("%s projection run = %s, want %s", label, projected.ID, snapshot.Run.ID)
		}
	}
}

// TestPlaybookRetryLoopE2E models the smallest useful loop as a bounded step
// retry rather than a cycle in the workflow graph:
//
//	validate (attempt 1 fails) -> needs_attention -> retry -> validate (attempt 2 succeeds)
//
// The graph remains acyclic, while the durable node-run ledger records both
// attempts and returns the run to running as soon as the retry is dispatched.
func TestPlaybookRetryLoopE2E(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "playbook-retry-leader", nil)
	workerID := createHandlerTestAgent(t, "playbook-retry-worker", nil)

	var squadID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO squad (workspace_id, name, description, leader_id, creator_id)
		VALUES ($1, 'Playbook Retry E2E Squad', '', $2, $3)
		RETURNING id
	`, testWorkspaceID, leaderID, testUserID).Scan(&squadID); err != nil {
		t.Fatalf("create squad: %v", err)
	}
	for role, agentID := range map[string]string{"leader": leaderID, "validator": workerID} {
		if _, err := testPool.Exec(ctx, `INSERT INTO squad_member (squad_id, member_type, member_id, role) VALUES ($1, 'agent', $2, $3)`, squadID, agentID, role); err != nil {
			t.Fatalf("add %s: %v", role, err)
		}
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE workspace_id = $1 AND title LIKE 'Playbook Retry E2E%'`, testWorkspaceID)
		testPool.Exec(context.Background(), `DELETE FROM squad WHERE id = $1`, squadID)
	})

	squad, err := testHandler.Queries.GetSquad(ctx, util.MustParseUUID(squadID))
	if err != nil {
		t.Fatalf("load squad: %v", err)
	}
	definition := fmt.Sprintf(`{
	  "version": 1,
	  "steps": [{
	    "key": "validate", "title": "Playbook Retry E2E validate", "agent_id": %q,
	    "instructions": "Validate the release candidate.", "max_attempts": 2,
	    "output_schema": {"type":"object","required":["valid"],"properties":{"valid":{"type":"boolean"}},"additional_properties":false}
	  }]
	}`, workerID)
	if _, err := testHandler.PlaybookService.SaveDefinition(ctx, squad, []byte(definition)); err != nil {
		t.Fatalf("save playbook: %v", err)
	}
	squad, _ = testHandler.Queries.GetSquad(ctx, util.MustParseUUID(squadID))
	rootResult, err := testHandler.IssueService.Create(ctx, service.IssueCreateParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID), Title: "Playbook Retry E2E root",
		Status: "backlog", Priority: "none", AssigneeType: pgtype.Text{String: "squad", Valid: true},
		AssigneeID: squad.ID, CreatorType: "member", CreatorID: util.MustParseUUID(testUserID),
	}, service.IssueCreateOpts{Platform: "test"})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	snapshot, err := testHandler.PlaybookService.StartRun(ctx, squad, rootResult.Issue.ID, util.MustParseUUID(testUserID), nil)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	first := findNode(t, snapshot.Nodes, "validate")
	if first.Attempt != 1 || first.Status != "running" {
		t.Fatalf("first attempt = %d/%s, want 1/running", first.Attempt, first.Status)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, first.TaskID); err != nil {
		t.Fatalf("claim first attempt: %v", err)
	}
	failed, err := testHandler.TaskService.FailTask(ctx, first.TaskID, "simulated validator crash", "", "", "agent_error")
	if err != nil {
		t.Fatalf("fail first attempt: %v", err)
	}
	if err := testHandler.PlaybookService.HandleTaskFailed(ctx, failed.ID, "simulated validator crash"); err != nil {
		t.Fatalf("project first failure: %v", err)
	}
	run, err := testHandler.Queries.GetWorkflowRun(ctx, snapshot.Run.ID)
	if err != nil {
		t.Fatalf("reload failed run: %v", err)
	}
	nodes, err := testHandler.Queries.ListWorkflowNodeRuns(ctx, run.ID)
	if err != nil {
		t.Fatalf("reload failed nodes: %v", err)
	}
	snapshot = service.PlaybookRunSnapshot{Run: run, Nodes: nodes}
	attention := findNode(t, snapshot.Nodes, "validate")
	if snapshot.Run.Status != "needs_attention" || attention.Status != "needs_attention" || attention.Error != "simulated validator crash" {
		t.Fatalf("failure projection = run %s, node %s, error %q", snapshot.Run.Status, attention.Status, attention.Error)
	}

	snapshot, err = testHandler.PlaybookService.DispatchStep(ctx, service.DispatchPlaybookStepParams{
		RunID: snapshot.Run.ID, StepKey: "validate",
	})
	if err != nil {
		t.Fatalf("retry step: %v", err)
	}
	second := findNode(t, snapshot.Nodes, "validate")
	if snapshot.Run.Status != "running" || second.Status != "running" || second.Attempt != 2 || second.TaskID == first.TaskID || second.IssueID != first.IssueID {
		t.Fatalf("retry projection = run %s, node %s attempt %d task_same=%v issue_same=%v", snapshot.Run.Status, second.Status, second.Attempt, second.TaskID == first.TaskID, second.IssueID == first.IssueID)
	}
	snapshot = completePlaybookNode(t, snapshot, "validate", `{"valid":true}`)
	if snapshot.Run.Status != "succeeded" || findNode(t, snapshot.Nodes, "validate").Attempt != 2 {
		t.Fatalf("completed retry = run %s attempt %d", snapshot.Run.Status, findNode(t, snapshot.Nodes, "validate").Attempt)
	}
}

func completePlaybookNode(t *testing.T, snapshot service.PlaybookRunSnapshot, key, output string) service.PlaybookRunSnapshot {
	t.Helper()
	node := findNode(t, snapshot.Nodes, key)
	if !node.TaskID.Valid {
		t.Fatalf("node %s has no task", key)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1`, node.TaskID); err != nil {
		t.Fatalf("claim node %s task: %v", key, err)
	}
	if _, err := testHandler.PlaybookService.AcceptOutput(context.Background(), node.TaskID, []byte(output)); err != nil {
		t.Fatalf("accept node %s output: %v", key, err)
	}
	if _, err := testHandler.TaskService.CompleteTask(context.Background(), node.TaskID, []byte(`{"output":"done"}`), "", ""); err != nil {
		t.Fatalf("complete node %s task: %v", key, err)
	}
	if err := testHandler.PlaybookService.HandleTaskCompleted(context.Background(), node.TaskID); err != nil {
		t.Fatalf("advance after node %s: %v", key, err)
	}
	run, err := testHandler.Queries.GetWorkflowRun(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	nodes, err := testHandler.Queries.ListWorkflowNodeRuns(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("reload nodes: %v", err)
	}
	return service.PlaybookRunSnapshot{Run: run, Nodes: nodes}
}

func findNode(t *testing.T, nodes []db.WorkflowNodeRun, key string) db.WorkflowNodeRun {
	t.Helper()
	for _, node := range nodes {
		if node.StepKey == key {
			return node
		}
	}
	t.Fatalf("node %s not found", key)
	return db.WorkflowNodeRun{}
}

func assertNodeStatus(t *testing.T, nodes []db.WorkflowNodeRun, key, want string) {
	t.Helper()
	if got := findNode(t, nodes, key).Status; got != want {
		t.Fatalf("node %s status = %s, want %s", key, got, want)
	}
}
