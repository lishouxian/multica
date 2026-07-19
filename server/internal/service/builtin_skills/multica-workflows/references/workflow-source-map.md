# Workflow skill source map

Where each claim in SKILL.md comes from. Update this file and SKILL.md in the
same PR as any behavior change (CLAUDE.md rule).

| Claim in SKILL.md | Source of truth |
| --- | --- |
| YAML schema, step types, routing rules, validation invariants | `server/internal/service/workflow_def.go` (`ParseWorkflowDef`, `validate`) |
| Transition semantics: stage promotion, enum routing + default, reject back-edge, retry/timeout/deadline escalation | `server/internal/service/workflow_advance.go` (`AdvanceWorkflow` reducer) + `workflow_advance_test.go` |
| Step issue markers (`workflow` metadata key, description footer), output contract, attempt titles | `server/internal/service/workflow.go` (`materializeSteps`, `buildStepDescription`) |
| Approval Done=approve / Cancelled=reject, reject reason = approver's last comment | `server/internal/service/workflow.go` (`snapshotSteps`) + `workflow_advance.go` (`workflowStepOutcome`) |
| Run state storage (root issue metadata `workflow_state`, string-encoded JSON) | `server/internal/service/workflow.go` (`saveRunState`, `ParseWorkflowRunState`) |
| Engine drive model (30s reconcile tick, poll-only) | `server/cmd/server/workflow_tick.go` |
| CLI commands and flags | `server/cmd/multica/cmd_workflow.go` |
| HTTP surface | `server/internal/handler/workflow.go`, routes in `server/cmd/server/router.go` |
| Run statuses (`running`/`paused`/`needs_attention`/`ejected`/`done`/`cancelled`) | `server/internal/service/workflow_advance.go` (constants) |
