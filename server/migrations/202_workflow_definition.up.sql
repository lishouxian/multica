-- Workflow definitions (feat/workflow-v0): reusable YAML playbooks that the
-- workflow engine compiles into staged issue trees. Run state deliberately
-- lives in the run root issue's metadata (key 'workflow_state'), NOT in a
-- table, so the database stays schema-compatible with upstream for everything
-- run-related; this table only stores the small, re-pushable definitions.
--
-- No foreign keys: new tables enforce their relationships in the application
-- layer (MUL-3515). Definitions are soft-deleted via archived_at so historic
-- runs can still name their source definition.
--
-- The (workspace_id, name) uniqueness index lives in migration 203: every
-- production index is built CONCURRENTLY in its own single-statement file.
CREATE TABLE workflow_definition (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    name         TEXT NOT NULL,
    -- Raw YAML source as pushed. Parsed/validated at push time and again at
    -- run time; runs additionally snapshot the source into their run state so
    -- in-flight runs are immune to definition edits.
    source       TEXT NOT NULL,
    created_by   UUID NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at  TIMESTAMPTZ
);
