-- Workflow step issues are execution projections, not ordinary business
-- sub-issues. Keep their provenance even if a run ledger is later removed so
-- child lists and progress calculations never expose internal execution rows.
ALTER TABLE issue DROP CONSTRAINT IF EXISTS issue_origin_type_check;
ALTER TABLE issue ADD CONSTRAINT issue_origin_type_check
    CHECK (origin_type IN ('autopilot', 'quick_create', 'lark_chat', 'slack_chat', 'agent_create', 'workflow'));

-- Backfill step issues created by the first playbook implementation, before
-- provenance was stamped at creation time.
UPDATE issue AS i
SET origin_type = 'workflow',
    origin_id = wnr.workflow_run_id
FROM workflow_node_run AS wnr
WHERE wnr.issue_id = i.id
  AND i.origin_type IS NULL;

-- Align existing step projections with their durable node ledger. A completed
-- workflow step is not waiting for a second issue-level review; the root issue
-- remains the collaboration surface for final review.
UPDATE issue AS i
SET status = 'done',
    updated_at = now()
FROM workflow_node_run AS wnr
WHERE wnr.issue_id = i.id
  AND wnr.status = 'succeeded'
  AND i.status NOT IN ('done', 'cancelled');

UPDATE issue AS i
SET status = 'blocked',
    updated_at = now()
FROM workflow_node_run AS wnr
WHERE wnr.issue_id = i.id
  AND wnr.status IN ('failed', 'needs_attention')
  AND i.status NOT IN ('done', 'cancelled');
