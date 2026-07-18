package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Workflow endpoints (feat/workflow-v0). Definitions are YAML playbooks
// pushed via CLI or UI; runs are staged issue trees whose state lives in the
// run root's metadata. The handler stays thin: parsing, authz scoping via
// workspace headers, and delegation to WorkflowService.

type workflowStepInfo struct {
	Key      string `json:"key"`
	Stage    int    `json:"stage"`
	Type     string `json:"type"`
	Assignee string `json:"assignee"`
	Title    string `json:"title,omitempty"`
}

type workflowDefinitionResponse struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Source    string             `json:"source"`
	Vars      []string           `json:"vars"`
	Steps     []workflowStepInfo `json:"steps"`
	CreatedBy string             `json:"created_by"`
	CreatedAt string             `json:"created_at"`
	UpdatedAt string             `json:"updated_at"`
}

func workflowDefinitionToResponse(row db.WorkflowDefinition) workflowDefinitionResponse {
	resp := workflowDefinitionResponse{
		ID:        uuidToString(row.ID),
		Name:      row.Name,
		Source:    row.Source,
		Vars:      []string{},
		Steps:     []workflowStepInfo{},
		CreatedBy: uuidToString(row.CreatedBy),
		CreatedAt: timestampToString(row.CreatedAt),
		UpdatedAt: timestampToString(row.UpdatedAt),
	}
	if def, err := service.ParseWorkflowDef(row.Source); err == nil {
		if def.Vars != nil {
			resp.Vars = def.Vars
		}
		for i := range def.Steps {
			s := &def.Steps[i]
			resp.Steps = append(resp.Steps, workflowStepInfo{
				Key: s.Key, Stage: s.Stage, Type: s.Type, Assignee: s.Assignee, Title: s.Title,
			})
		}
	}
	return resp
}

type workflowRunAttempt struct {
	IssueID     string `json:"issue_id"`
	Identifier  string `json:"identifier"`
	N           int    `json:"n"`
	IssueStatus string `json:"issue_status"`
	CreatedAt   string `json:"created_at"`
}

type workflowRunStep struct {
	Key       string               `json:"key"`
	Stage     int                  `json:"stage"`
	Type      string               `json:"type"`
	Assignee  string               `json:"assignee"`
	Active    bool                 `json:"active"`
	LoopsUsed int                  `json:"loops_used"`
	Attempts  []workflowRunAttempt `json:"attempts"`
}

type workflowRunResponse struct {
	RootIssueID    string            `json:"root_issue_id"`
	RootIdentifier string            `json:"root_identifier"`
	RootTitle      string            `json:"root_title"`
	Workflow       string            `json:"workflow"`
	DefinitionID   string            `json:"definition_id,omitempty"`
	DefSHA         string            `json:"def_sha"`
	Status         string            `json:"status"`
	StatusReason   string            `json:"status_reason,omitempty"`
	Vars           map[string]string `json:"vars,omitempty"`
	Frontier       []string          `json:"frontier"`
	Steps          []workflowRunStep `json:"steps"`
	StartedAt      string            `json:"started_at"`
	UpdatedAt      string            `json:"updated_at"`
}

func (h *Handler) workflowRunToResponse(r *http.Request, root db.Issue, state *service.WorkflowRunState) workflowRunResponse {
	prefix := h.workflowIssuePrefix(r.Context(), root.WorkspaceID)
	resp := workflowRunResponse{
		RootIssueID:    uuidToString(root.ID),
		RootIdentifier: prefix + "-" + strconv.Itoa(int(root.Number)),
		RootTitle:      root.Title,
		Workflow:       state.Workflow,
		DefinitionID:   state.DefinitionID,
		DefSHA:         state.DefSHA,
		Status:         state.Status,
		StatusReason:   state.StatusReason,
		Vars:           state.Vars,
		Frontier:       state.Frontier,
		Steps:          []workflowRunStep{},
		StartedAt:      state.StartedAt.UTC().Format(time.RFC3339),
		UpdatedAt:      state.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if resp.Frontier == nil {
		resp.Frontier = []string{}
	}
	frontier := map[string]bool{}
	for _, k := range state.Frontier {
		frontier[k] = true
	}
	def, err := service.ParseWorkflowDef(state.DefSource)
	if err != nil {
		return resp
	}
	for i := range def.Steps {
		stepDef := &def.Steps[i]
		step := workflowRunStep{
			Key: stepDef.Key, Stage: stepDef.Stage, Type: stepDef.Type,
			Assignee: stepDef.Assignee, Active: frontier[stepDef.Key],
			Attempts: []workflowRunAttempt{},
		}
		if st := state.Steps[stepDef.Key]; st != nil {
			step.LoopsUsed = st.LoopsUsed
			for _, attempt := range st.Attempts {
				a := workflowRunAttempt{
					IssueID:   attempt.IssueID,
					N:         attempt.N,
					CreatedAt: attempt.CreatedAt.UTC().Format(time.RFC3339),
				}
				if issueID, err := parseUUIDSafe(attempt.IssueID); err == nil {
					if issue, err := h.Queries.GetIssue(r.Context(), issueID); err == nil {
						a.IssueStatus = issue.Status
						a.Identifier = prefix + "-" + strconv.Itoa(int(issue.Number))
					}
				}
				step.Attempts = append(step.Attempts, a)
			}
		}
		resp.Steps = append(resp.Steps, step)
	}
	return resp
}

func (h *Handler) workflowIssuePrefix(ctx context.Context, wsID pgtype.UUID) string {
	ws, err := h.Queries.GetWorkspace(ctx, wsID)
	if err != nil {
		return "ISSUE"
	}
	return ws.IssuePrefix
}

// ---- Definitions ----

type workflowPushRequest struct {
	Source string `json:"source"`
}

// ValidateWorkflow parses YAML source without persisting anything.
func (h *Handler) ValidateWorkflow(w http.ResponseWriter, r *http.Request) {
	var req workflowPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Source == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}
	def, err := service.ParseWorkflowDef(req.Source)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	steps := []workflowStepInfo{}
	for i := range def.Steps {
		s := &def.Steps[i]
		steps = append(steps, workflowStepInfo{Key: s.Key, Stage: s.Stage, Type: s.Type, Assignee: s.Assignee, Title: s.Title})
	}
	writeJSON(w, http.StatusOK, map[string]any{"valid": true, "name": def.Name, "steps": steps})
}

// PushWorkflow validates and upserts a definition by its YAML-declared name.
func (h *Handler) PushWorkflow(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := parseUUIDOrBadRequest(w, requestUserID(r), "user id")
	if !ok {
		return
	}
	var req workflowPushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Source == "" {
		writeError(w, http.StatusBadRequest, "source is required")
		return
	}
	row, _, err := h.WorkflowService.PushDefinition(r.Context(), parseUUID(workspaceID), userID, req.Source)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workflow: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, workflowDefinitionToResponse(row))
}

func (h *Handler) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	rows, err := h.Queries.ListWorkflowDefinitions(r.Context(), parseUUID(workspaceID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workflows")
		return
	}
	out := []workflowDefinitionResponse{}
	for _, row := range rows {
		out = append(out, workflowDefinitionToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": out})
}

func (h *Handler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workflow id")
	if !ok {
		return
	}
	row, err := h.Queries.GetWorkflowDefinitionInWorkspace(r.Context(), db.GetWorkflowDefinitionInWorkspaceParams{
		ID: id, WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}
	writeJSON(w, http.StatusOK, workflowDefinitionToResponse(row))
}

func (h *Handler) ArchiveWorkflow(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workflow id")
	if !ok {
		return
	}
	if _, err := h.Queries.ArchiveWorkflowDefinition(r.Context(), db.ArchiveWorkflowDefinitionParams{
		ID: id, WorkspaceID: parseUUID(workspaceID),
	}); err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"archived": true})
}

// ---- Runs ----

type workflowRunRequest struct {
	Vars map[string]string `json:"vars"`
}

func (h *Handler) RunWorkflow(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	userID, ok := parseUUIDOrBadRequest(w, requestUserID(r), "user id")
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workflow id")
	if !ok {
		return
	}
	var req workflowRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	row, err := h.Queries.GetWorkflowDefinitionInWorkspace(r.Context(), db.GetWorkflowDefinitionInWorkspaceParams{
		ID: id, WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "workflow not found")
		return
	}
	root, err := h.WorkflowService.StartRun(r.Context(), row, req.Vars, userID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to start run: "+err.Error())
		return
	}
	// Re-read the root: StartRun mutates metadata after creation.
	root, err = h.Queries.GetIssue(r.Context(), root.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "run started but root re-read failed")
		return
	}
	state, err := service.ParseWorkflowRunState(root.Metadata)
	if err != nil || state == nil {
		writeError(w, http.StatusInternalServerError, "run started but state is unreadable")
		return
	}
	writeJSON(w, http.StatusOK, h.workflowRunToResponse(r, root, state))
}

func (h *Handler) ListWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	rows, err := h.Queries.ListWorkflowRunRootsInWorkspace(r.Context(), db.ListWorkflowRunRootsInWorkspaceParams{
		WorkspaceID: parseUUID(workspaceID),
		Limit:       100,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}
	out := []workflowRunResponse{}
	for _, row := range rows {
		issue, err := h.Queries.GetIssue(r.Context(), row.ID)
		if err != nil {
			continue
		}
		state, err := service.ParseWorkflowRunState(issue.Metadata)
		if err != nil || state == nil {
			continue
		}
		out = append(out, h.workflowRunToResponse(r, issue, state))
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// loadRunRootForWorkspace scopes a run root to the request workspace before
// any control operation touches it.
func (h *Handler) loadRunRootForWorkspace(w http.ResponseWriter, r *http.Request) (db.Issue, *service.WorkflowRunState, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "run root issue id")
	if !ok {
		return db.Issue{}, nil, false
	}
	issue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID: id, WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "run not found")
		return db.Issue{}, nil, false
	}
	state, err := service.ParseWorkflowRunState(issue.Metadata)
	if err != nil || state == nil {
		writeError(w, http.StatusNotFound, "issue is not a workflow run root")
		return db.Issue{}, nil, false
	}
	return issue, state, true
}

func (h *Handler) GetWorkflowRun(w http.ResponseWriter, r *http.Request) {
	issue, state, ok := h.loadRunRootForWorkspace(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.workflowRunToResponse(r, issue, state))
}

func (h *Handler) workflowRunControl(w http.ResponseWriter, r *http.Request, op func(pgtype.UUID) error) {
	issue, _, ok := h.loadRunRootForWorkspace(w, r)
	if !ok {
		return
	}
	if err := op(issue.ID); err != nil {
		if errors.Is(err, service.ErrWorkflowRunState) {
			writeError(w, http.StatusConflict, "run is not in a state that allows this operation")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	issue, err := h.Queries.GetIssue(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state changed but re-read failed")
		return
	}
	state, err := service.ParseWorkflowRunState(issue.Metadata)
	if err != nil || state == nil {
		writeError(w, http.StatusInternalServerError, "state changed but is unreadable")
		return
	}
	writeJSON(w, http.StatusOK, h.workflowRunToResponse(r, issue, state))
}

func (h *Handler) PauseWorkflowRun(w http.ResponseWriter, r *http.Request) {
	h.workflowRunControl(w, r, func(id pgtype.UUID) error { return h.WorkflowService.PauseRun(r.Context(), id) })
}

func (h *Handler) ResumeWorkflowRun(w http.ResponseWriter, r *http.Request) {
	h.workflowRunControl(w, r, func(id pgtype.UUID) error { return h.WorkflowService.ResumeRun(r.Context(), id) })
}

func (h *Handler) CancelWorkflowRun(w http.ResponseWriter, r *http.Request) {
	h.workflowRunControl(w, r, func(id pgtype.UUID) error { return h.WorkflowService.CancelRun(r.Context(), id) })
}

func (h *Handler) EjectWorkflowRun(w http.ResponseWriter, r *http.Request) {
	h.workflowRunControl(w, r, func(id pgtype.UUID) error { return h.WorkflowService.EjectRun(r.Context(), id) })
}

type workflowRetryRequest struct {
	Step string `json:"step"`
}

func (h *Handler) RetryWorkflowStep(w http.ResponseWriter, r *http.Request) {
	var req workflowRetryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Step == "" {
		writeError(w, http.StatusBadRequest, "step is required")
		return
	}
	h.workflowRunControl(w, r, func(id pgtype.UUID) error {
		return h.WorkflowService.RetryStep(r.Context(), id, req.Step)
	})
}

// parseUUIDSafe is the non-panicking variant for ids read back from run
// state JSON (engine-written, but defensively parsed).
func parseUUIDSafe(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return u, nil
}
