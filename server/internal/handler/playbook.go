package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type playbookDefinitionResponse struct {
	ID         string          `json:"id"`
	SquadID    string          `json:"squad_id"`
	Name       string          `json:"name"`
	Version    int32           `json:"version"`
	Definition json.RawMessage `json:"definition"`
	UpdatedAt  string          `json:"updated_at"`
}

type playbookRunResponse struct {
	ID                        string                    `json:"id"`
	WorkflowDefinitionID      string                    `json:"workflow_definition_id"`
	WorkflowDefinitionVersion int32                     `json:"workflow_definition_version"`
	SquadID                   string                    `json:"squad_id"`
	RootIssueID               string                    `json:"root_issue_id"`
	Status                    string                    `json:"status"`
	Context                   json.RawMessage           `json:"context"`
	CreatedAt                 string                    `json:"created_at"`
	UpdatedAt                 string                    `json:"updated_at"`
	CompletedAt               *string                   `json:"completed_at"`
	Nodes                     []playbookNodeRunResponse `json:"nodes"`
}

type playbookNodeRunResponse struct {
	ID             string          `json:"id"`
	StepKey        string          `json:"step_key"`
	AgentID        string          `json:"agent_id"`
	IssueID        *string         `json:"issue_id"`
	TaskID         *string         `json:"task_id"`
	AcceptedTaskID *string         `json:"accepted_task_id"`
	Status         string          `json:"status"`
	InputSnapshot  json.RawMessage `json:"input_snapshot"`
	Output         json.RawMessage `json:"output"`
	Attempt        int32           `json:"attempt"`
	Error          string          `json:"error"`
	UpdatedAt      string          `json:"updated_at"`
}

func playbookDefinitionToResponse(row db.WorkflowDefinition) playbookDefinitionResponse {
	return playbookDefinitionResponse{
		ID:         uuidToString(row.ID),
		SquadID:    uuidToString(row.SquadID),
		Name:       row.Name,
		Version:    row.Version,
		Definition: json.RawMessage(row.Definition),
		UpdatedAt:  timestampToString(row.UpdatedAt),
	}
}

func playbookSnapshotToResponse(snapshot service.PlaybookRunSnapshot) playbookRunResponse {
	run := snapshot.Run
	response := playbookRunResponse{
		ID:                        uuidToString(run.ID),
		WorkflowDefinitionID:      uuidToString(run.WorkflowDefinitionID),
		WorkflowDefinitionVersion: run.WorkflowDefinitionVersion,
		SquadID:                   uuidToString(run.SquadID),
		RootIssueID:               uuidToString(run.RootIssueID),
		Status:                    run.Status,
		Context:                   json.RawMessage(run.Context),
		CreatedAt:                 timestampToString(run.CreatedAt),
		UpdatedAt:                 timestampToString(run.UpdatedAt),
		CompletedAt:               timestampToPtr(run.CompletedAt),
		Nodes:                     make([]playbookNodeRunResponse, 0, len(snapshot.Nodes)),
	}
	for _, node := range snapshot.Nodes {
		response.Nodes = append(response.Nodes, playbookNodeRunResponse{
			ID:             uuidToString(node.ID),
			StepKey:        node.StepKey,
			AgentID:        uuidToString(node.AgentID),
			IssueID:        uuidToPtr(node.IssueID),
			TaskID:         uuidToPtr(node.TaskID),
			AcceptedTaskID: uuidToPtr(node.AcceptedTaskID),
			Status:         node.Status,
			InputSnapshot:  json.RawMessage(node.InputSnapshot),
			Output:         json.RawMessage(node.Output),
			Attempt:        node.Attempt,
			Error:          node.Error,
			UpdatedAt:      timestampToString(node.UpdatedAt),
		})
	}
	return response
}

func (h *Handler) GetSquadPlaybook(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	if !squad.WorkflowDefinitionID.Valid {
		writeJSON(w, http.StatusOK, map[string]any{"orchestration_mode": "leader", "definition": nil})
		return
	}
	row, err := h.Queries.GetWorkflowDefinition(r.Context(), squad.WorkflowDefinitionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load squad playbook")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orchestration_mode": squad.OrchestrationMode, "definition": playbookDefinitionToResponse(row)})
}

func (h *Handler) SaveSquadPlaybook(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	if !canManageSquad(member, squad) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	var req struct {
		Definition json.RawMessage `json:"definition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Definition) == 0 {
		writeError(w, http.StatusBadRequest, "definition is required")
		return
	}
	row, err := h.PlaybookService.SaveDefinition(r.Context(), squad, req.Definition)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orchestration_mode": "playbook", "definition": playbookDefinitionToResponse(row)})
}

func (h *Handler) DisableSquadPlaybook(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	if !canManageSquad(member, squad) {
		writeError(w, http.StatusForbidden, "insufficient permissions")
		return
	}
	if _, err := h.Queries.DisableSquadPlaybook(r.Context(), squad.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to disable squad playbook")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orchestration_mode": "leader", "definition": nil})
}

func (h *Handler) StartSquadPlaybookRun(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	var req struct {
		RootIssueID string          `json:"root_issue_id"`
		Context     json.RawMessage `json:"context"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rootIssueID, ok := parseUUIDOrBadRequest(w, req.RootIssueID, "root_issue_id")
	if !ok {
		return
	}
	snapshot, err := h.PlaybookService.StartRun(r.Context(), squad, rootIssueID, member.UserID, req.Context)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, playbookSnapshotToResponse(snapshot))
}

func (h *Handler) ListSquadPlaybookRuns(w http.ResponseWriter, r *http.Request) {
	squad, workspaceID, ok := h.loadSquadInWorkspace(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	limit := int32(20)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 && value <= 100 {
			limit = int32(value)
		}
	}
	rows, err := h.Queries.ListSquadWorkflowRuns(r.Context(), db.ListSquadWorkflowRunsParams{SquadID: squad.ID, WorkspaceID: squad.WorkspaceID, Limit: limit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list playbook runs")
		return
	}
	responses := make([]playbookRunResponse, 0, len(rows))
	for _, run := range rows {
		nodes, err := h.Queries.ListWorkflowNodeRuns(r.Context(), run.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load playbook run")
			return
		}
		responses = append(responses, playbookSnapshotToResponse(service.PlaybookRunSnapshot{Run: run, Nodes: nodes}))
	}
	writeJSON(w, http.StatusOK, responses)
}

func (h *Handler) GetPlaybookRun(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	if _, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found"); !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "runId"), "run_id")
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	run, err := h.Queries.GetWorkflowRunInWorkspace(r.Context(), db.GetWorkflowRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "playbook run not found")
		return
	}
	nodes, err := h.Queries.ListWorkflowNodeRuns(r.Context(), run.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load playbook run")
		return
	}
	writeJSON(w, http.StatusOK, playbookSnapshotToResponse(service.PlaybookRunSnapshot{Run: run, Nodes: nodes}))
}

func (h *Handler) DispatchPlaybookStep(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "workspace not found")
	if !ok {
		return
	}
	runID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "runId"), "run_id")
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	run, err := h.Queries.GetWorkflowRunInWorkspace(r.Context(), db.GetWorkflowRunInWorkspaceParams{ID: runID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "playbook run not found")
		return
	}
	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{ID: run.SquadID, WorkspaceID: workspaceUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "squad not found")
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	isLeader := actorType == "agent" && actorID == uuidToString(squad.LeaderID)
	if !isLeader && !canManageSquad(member, squad) {
		writeError(w, http.StatusForbidden, "only the squad leader or manager can dispatch playbook steps")
		return
	}
	var req struct {
		StepKey string          `json:"step_key"`
		AgentID string          `json:"agent_id"`
		Input   json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.StepKey == "" {
		writeError(w, http.StatusBadRequest, "step_key is required")
		return
	}
	var agentID pgtype.UUID
	if req.AgentID != "" {
		agentID, ok = parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
		if !ok {
			return
		}
	}
	snapshot, err := h.PlaybookService.DispatchStep(r.Context(), service.DispatchPlaybookStepParams{RunID: run.ID, StepKey: req.StepKey, AgentID: agentID, InputOverride: req.Input})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, playbookSnapshotToResponse(snapshot))
}

func (h *Handler) SetPlaybookTaskOutput(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task_id")
	if !ok {
		return
	}
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	actorType, _ := h.resolveActor(r, requestUserID(r), workspaceID)
	if actorType != "agent" || r.Header.Get("X-Task-ID") != uuidToString(taskID) {
		writeError(w, http.StatusForbidden, "only the current task can submit playbook output")
		return
	}
	var req struct {
		Output json.RawMessage `json:"output"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Output) == 0 {
		writeError(w, http.StatusBadRequest, "output is required")
		return
	}
	node, err := h.PlaybookService.AcceptOutput(r.Context(), taskID, req.Output)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"step_key": node.StepKey, "status": "accepted", "task_id": uuidToString(taskID)})
}
