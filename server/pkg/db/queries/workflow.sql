-- Workflow definitions (feat/workflow-v0). Run state lives in the run root
-- issue's metadata key 'workflow_state' (see service/workflow.go), so the only
-- workflow-owned rows are the definitions themselves plus two issue-side
-- helpers for the reconcile tick.

-- name: CreateWorkflowDefinition :one
INSERT INTO workflow_definition (workspace_id, name, source, created_by)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateWorkflowDefinitionSource :one
UPDATE workflow_definition
SET source = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND archived_at IS NULL
RETURNING *;

-- name: GetWorkflowDefinitionInWorkspace :one
SELECT * FROM workflow_definition
WHERE id = $1 AND workspace_id = $2 AND archived_at IS NULL;

-- name: GetWorkflowDefinitionByName :one
SELECT * FROM workflow_definition
WHERE workspace_id = $1 AND name = $2 AND archived_at IS NULL;

-- name: ListWorkflowDefinitions :many
SELECT * FROM workflow_definition
WHERE workspace_id = $1 AND archived_at IS NULL
ORDER BY name ASC;

-- name: ArchiveWorkflowDefinition :one
UPDATE workflow_definition
SET archived_at = now(), updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND archived_at IS NULL
RETURNING *;

-- name: ListWorkflowRunRoots :many
-- Every issue that carries workflow run state, newest first. The reconcile
-- tick loads all of them and filters on parsed state in Go (running vs
-- terminal); the partial index idx_issue_workflow_run_root keeps this cheap.
-- Scans across every workspace: the tick is a process-wide loop, and each
-- returned row carries its workspace_id for the per-run scoping that follows.
SELECT i.id, i.workspace_id, i.title, i.status, i.assignee_type, i.assignee_id,
       i.creator_type, i.creator_id, i.parent_issue_id, i.number, i.project_id,
       i.metadata, i.stage, i.created_at, i.updated_at
FROM issue i
WHERE i.metadata ? 'workflow_state'
ORDER BY i.created_at DESC;

-- name: UpdateWorkflowIssueDescription :one
-- Engine-owned rewrite of a run root's progress block. Kept narrow on purpose:
-- the engine must never touch fields it does not own.
UPDATE issue SET description = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateWorkflowIssueStatus :one
-- Engine-owned status flips (run root lifecycle + run-cancel cleanup of open
-- step issues). Handler-side status flows keep their own path; the engine
-- reads outcomes by polling, so it does not depend on handler side effects.
UPDATE issue SET status = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: GetLatestMemberCommentForIssue :one
-- The rejection reason of an approval step: the approver's last words on the
-- approval issue.
SELECT * FROM comment
WHERE issue_id = $1 AND author_type = 'member' AND type != 'system'
ORDER BY created_at DESC
LIMIT 1;

-- name: ListWorkflowRunRootsInWorkspace :many
SELECT i.id, i.workspace_id, i.title, i.status, i.assignee_type, i.assignee_id,
       i.creator_type, i.creator_id, i.parent_issue_id, i.number, i.project_id,
       i.metadata, i.stage, i.created_at, i.updated_at
FROM issue i
WHERE i.workspace_id = $1 AND i.metadata ? 'workflow_state'
ORDER BY i.created_at DESC
LIMIT $2;
