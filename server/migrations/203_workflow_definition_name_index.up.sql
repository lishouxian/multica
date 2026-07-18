-- One live definition per (workspace, name): `multica workflow push` upserts
-- by name, and archived rows are excluded so a name can be reused after
-- archiving. Kept as the migration's only statement: PostgreSQL rejects
-- CREATE INDEX CONCURRENTLY inside a transaction or multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_workflow_definition_ws_name
    ON workflow_definition (workspace_id, name) WHERE archived_at IS NULL;
