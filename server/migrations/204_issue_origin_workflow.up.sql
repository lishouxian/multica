-- Extend issue.origin_type to allow the workflow engine to stamp step issues
-- with origin_type='workflow' + origin_id=<workflow_definition.id>. The engine
-- uses the run root's metadata for orchestration state; the origin stamp is
-- for provenance/audit ("this issue was materialized by playbook X"), matching
-- how autopilot-created issues carry origin_type='autopilot'. Mirrors 149
-- (agent_create).
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'workflow'));
