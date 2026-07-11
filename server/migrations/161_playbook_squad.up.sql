-- A squad-private workflow definition keeps the first product surface inside
-- squads while preserving the durable workflow ledger needed for handoffs.
CREATE TABLE workflow_definition (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    squad_id UUID NOT NULL REFERENCES squad(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    definition JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(squad_id)
);

CREATE INDEX idx_workflow_definition_workspace ON workflow_definition(workspace_id);

ALTER TABLE squad
    ADD COLUMN orchestration_mode TEXT NOT NULL DEFAULT 'leader'
        CHECK (orchestration_mode IN ('leader', 'playbook')),
    ADD COLUMN workflow_definition_id UUID REFERENCES workflow_definition(id) ON DELETE SET NULL;

CREATE TABLE workflow_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_definition_id UUID NOT NULL REFERENCES workflow_definition(id) ON DELETE RESTRICT,
    workflow_definition_version INTEGER NOT NULL,
    definition_snapshot JSONB NOT NULL,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    squad_id UUID NOT NULL REFERENCES squad(id) ON DELETE RESTRICT,
    root_issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'running'
        CHECK (status IN ('running', 'succeeded', 'needs_attention', 'cancelled')),
    context JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX idx_workflow_run_active_root
    ON workflow_run(squad_id, root_issue_id)
    WHERE status IN ('running', 'needs_attention');
CREATE INDEX idx_workflow_run_squad_created ON workflow_run(squad_id, created_at DESC);

CREATE TABLE workflow_node_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workflow_run_id UUID NOT NULL REFERENCES workflow_run(id) ON DELETE CASCADE,
    step_key TEXT NOT NULL,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE RESTRICT,
    issue_id UUID REFERENCES issue(id) ON DELETE SET NULL,
    task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
    accepted_task_id UUID REFERENCES agent_task_queue(id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'ready', 'running', 'succeeded', 'failed', 'skipped', 'needs_attention', 'cancelled')),
    input_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    output JSONB,
    output_schema_version INTEGER NOT NULL DEFAULT 1,
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    UNIQUE(workflow_run_id, step_key)
);

CREATE UNIQUE INDEX idx_workflow_node_run_task
    ON workflow_node_run(task_id)
    WHERE task_id IS NOT NULL;
CREATE INDEX idx_workflow_node_run_run ON workflow_node_run(workflow_run_id, created_at);
