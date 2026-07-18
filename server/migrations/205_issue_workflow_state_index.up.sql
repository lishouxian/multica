-- Back the workflow reconcile tick's run-root scan ("every issue that carries
-- workflow run state"). Partial on the metadata key so the index stays tiny —
-- only run roots qualify — and the 30s tick never seq-scans the issue table.
-- Kept as the migration's only statement: PostgreSQL rejects CREATE INDEX
-- CONCURRENTLY inside a transaction or multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_workflow_run_root
    ON issue (workspace_id, created_at DESC) WHERE metadata ? 'workflow_state';
