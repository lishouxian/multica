DROP TABLE IF EXISTS workflow_node_run;
DROP TABLE IF EXISTS workflow_run;

ALTER TABLE squad
    DROP COLUMN IF EXISTS workflow_definition_id,
    DROP COLUMN IF EXISTS orchestration_mode;

DROP TABLE IF EXISTS workflow_definition;
