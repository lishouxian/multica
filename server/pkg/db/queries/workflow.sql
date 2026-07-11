-- name: UpsertSquadWorkflowDefinition :one
INSERT INTO workflow_definition (workspace_id, squad_id, name, definition)
VALUES ($1, $2, $3, $4)
ON CONFLICT (squad_id) DO UPDATE SET
    name = EXCLUDED.name,
    definition = EXCLUDED.definition,
    version = workflow_definition.version + 1,
    updated_at = now()
RETURNING *;

-- name: GetWorkflowDefinition :one
SELECT * FROM workflow_definition WHERE id = $1;

-- name: GetSquadWorkflowDefinition :one
SELECT * FROM workflow_definition WHERE squad_id = $1 AND workspace_id = $2;

-- name: EnableSquadPlaybook :one
UPDATE squad SET
    orchestration_mode = 'playbook',
    workflow_definition_id = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DisableSquadPlaybook :one
UPDATE squad SET
    orchestration_mode = 'leader',
    workflow_definition_id = NULL,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: CreateWorkflowRun :one
INSERT INTO workflow_run (
    workflow_definition_id, workflow_definition_version, definition_snapshot,
    workspace_id, squad_id, root_issue_id, context, created_by
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetWorkflowRun :one
SELECT * FROM workflow_run WHERE id = $1;

-- name: GetWorkflowRunInWorkspace :one
SELECT * FROM workflow_run WHERE id = $1 AND workspace_id = $2;

-- name: GetActiveWorkflowRunForRoot :one
SELECT * FROM workflow_run
WHERE squad_id = $1 AND root_issue_id = $2
  AND status IN ('running', 'needs_attention')
ORDER BY created_at DESC
LIMIT 1;

-- name: ListSquadWorkflowRuns :many
SELECT * FROM workflow_run
WHERE squad_id = $1 AND workspace_id = $2
ORDER BY created_at DESC
LIMIT $3;

-- name: UpdateWorkflowRunStatus :one
UPDATE workflow_run SET
    status = $2,
    updated_at = now(),
    completed_at = CASE WHEN $2 IN ('succeeded', 'cancelled') THEN now() ELSE NULL END
WHERE id = $1
RETURNING *;

-- name: CreateWorkflowNodeRun :one
INSERT INTO workflow_node_run (workflow_run_id, step_key, agent_id)
VALUES ($1, $2, $3)
ON CONFLICT (workflow_run_id, step_key) DO UPDATE SET
    agent_id = workflow_node_run.agent_id
RETURNING *;

-- name: GetWorkflowNodeRun :one
SELECT * FROM workflow_node_run WHERE id = $1;

-- name: GetWorkflowNodeRunByTask :one
SELECT * FROM workflow_node_run WHERE task_id = $1;

-- name: GetWorkflowNodeRunByKey :one
SELECT * FROM workflow_node_run WHERE workflow_run_id = $1 AND step_key = $2;

-- name: ListWorkflowNodeRuns :many
SELECT * FROM workflow_node_run
WHERE workflow_run_id = $1
ORDER BY created_at ASC;

-- name: MarkWorkflowNodeReady :one
UPDATE workflow_node_run SET
    status = 'ready',
    input_snapshot = $2,
    updated_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: MarkWorkflowNodeRunning :one
UPDATE workflow_node_run SET
    status = 'running',
    agent_id = $2,
    issue_id = $3,
    task_id = $4,
    attempt = attempt + 1,
    error = '',
    updated_at = now()
WHERE id = $1 AND status IN ('ready', 'needs_attention')
RETURNING *;

-- name: AcceptWorkflowNodeOutput :one
UPDATE workflow_node_run SET
    output = $2,
    accepted_task_id = $3,
    output_schema_version = $4,
    updated_at = now()
WHERE id = $1 AND task_id = $3 AND status = 'running'
RETURNING *;

-- name: MarkWorkflowNodeSucceeded :one
UPDATE workflow_node_run SET
    status = 'succeeded',
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND status = 'running' AND output IS NOT NULL
RETURNING *;

-- name: MarkWorkflowNodeSkipped :one
UPDATE workflow_node_run SET
    status = 'skipped',
    completed_at = now(),
    updated_at = now()
WHERE id = $1 AND status = 'pending'
RETURNING *;

-- name: MarkWorkflowNodeNeedsAttention :one
UPDATE workflow_node_run SET
    status = 'needs_attention',
    error = $2,
    updated_at = now()
WHERE id = $1 AND status IN ('pending', 'ready', 'running', 'failed')
RETURNING *;

-- name: RebindWorkflowNodeRetryTask :one
UPDATE workflow_node_run SET
    task_id = $2,
    accepted_task_id = NULL,
    output = NULL,
    attempt = attempt + 1,
    error = '',
    updated_at = now()
WHERE id = $1 AND task_id = $3 AND status = 'running'
RETURNING *;

-- name: ResetWorkflowNodeForRetry :one
UPDATE workflow_node_run SET
    status = 'ready',
    agent_id = $2,
    issue_id = NULL,
    task_id = NULL,
    accepted_task_id = NULL,
    output = NULL,
    input_snapshot = $3,
    error = '',
    completed_at = NULL,
    updated_at = now()
WHERE id = $1 AND status = 'needs_attention'
RETURNING *;

-- name: GetLatestAgentTaskForIssue :one
SELECT * FROM agent_task_queue
WHERE issue_id = $1 AND agent_id = $2
ORDER BY created_at DESC
LIMIT 1;
