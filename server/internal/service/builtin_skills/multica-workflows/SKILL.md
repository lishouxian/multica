---
name: multica-workflows
description: "Use when working on a workflow step issue, recording workflow step outputs, or managing workflow playbooks and runs via the multica CLI. Explains what workflows are, how step issues behave, the output contract, approval semantics, and the workflow push/run/status/pause/resume/cancel/eject/retry commands."
user-invocable: false
allowed-tools: Bash(multica *)
---

# Multica Workflows

A workflow compiles a YAML playbook into a staged issue tree. Steps sharing a
stage run in parallel as sibling sub-issues; the **engine** — not you —
promotes stages, routes on step outputs, loops back on rejected approvals,
and escalates failures to a human.

## Am I inside a workflow step?

A workflow step issue has:

- a `workflow` metadata key (`multica issue metadata get <id> workflow`),
- a footer in its description: *"Step `<key>` of workflow **<name>** · run
  root <IDENT> · transitions are engine-managed"*.

If either is present, the rules below apply.

## Rules for working on a workflow step issue

1. **Do your step, then set THIS issue to done.** That is the only signal the
   engine needs.
2. **Never promote, create, or close sibling issues** under the same run
   root. The engine owns every transition; a manual promote causes double
   activation. This overrides the promote guidance in
   multica-working-on-issues for engine-managed trees.
3. **Record required outputs before setting the issue to done.** If the
   description has a "Required outputs" section, write each value exactly as
   instructed:

   ```bash
   multica issue metadata set <this-issue-id> --key <output-key> --value "<enum-value>"
   ```

   The engine routes on these values. A missing or off-enum value does not
   crash the run — it takes the playbook's default branch, which usually
   means a human gets pulled in. Write the output; do not improvise new enum
   values.
4. **If you cannot finish the step**, set the issue to `cancelled` and leave
   a comment explaining why. The engine treats cancelled as failure and
   applies the playbook's retry/escalation policy. Do not leave the issue
   parked in `in_progress` — that only burns the step timeout.
5. Attempt issues are immutable history: a rework pass arrives as a NEW
   issue titled "... (attempt N)" with the rejection reason embedded. Work in
   the newest attempt only.

## Approval steps (humans)

An approval issue says "Approval required" and is assigned to a member:

- **Approve** = set the issue status to **Done**.
- **Reject** = set the issue status to **Cancelled** and leave a comment with
  the reason — the comment becomes the rework instruction for the loop-back
  pass.

## CLI

```bash
multica workflow validate <file.yaml>      # parse + graph checks, no save
multica workflow push <file.yaml>          # upsert definition (keyed by YAML name)
multica workflow list
multica workflow run <name> --var version=1.2
multica workflow runs                      # list runs
multica workflow status <run-root-issue>   # steps, attempts, frontier
multica workflow pause|resume|cancel|eject <run-root-issue>
multica workflow retry <run-root-issue> --step <key>
```

- `pause`: in-flight steps finish, nothing new starts. `resume` continues,
  and also un-parks a `needs_attention` run after a human fixed the cause.
- `cancel`: cancels the run AND its open step issues.
- `eject`: the engine lets go permanently; the tree becomes a plain issue
  tree to manage manually.
- `retry --step`: materializes a fresh attempt for one step and points the
  run at it.

## Playbook YAML in one glance

```yaml
name: release-flow
vars: [version]
policies: { run_deadline: 72h }
steps:
  - key: regression            # snake_case, unique
    stage: 1                   # same stage = parallel siblings
    assignee: "qa-agent"       # agent/member name; "agent:x" / "member:x" to pin
    prompt: "Run regression for {{vars.version}}."
    outputs: { result: [pass, fail] }   # written via issue metadata
    timeout: 4h
    on_fail: { retry: 1 }      # or { goto: <step> }; default = escalate to human
    next:
      when: "steps.regression.outputs.result"
      routes: { pass: gate }
      default: needs_attention # mandatory: enum drift downgrades, not crashes
  - key: gate
    stage: 2
    type: approval             # Done=approve, Cancelled=reject (+ reason comment)
    assignee: "member:owner"
    on_reject: { back_to: regression, max_loops: 2 }
  - key: ship
    stage: 3
    assignee: "ops-agent"
    prompt: "Ship {{vars.version}}. Result: {{steps.regression.outputs.result}}."
```

Interpolation scope: `{{vars.*}}`, `{{steps.<key>.outputs.<name>}}`,
`{{steps.<key>.outcome}}`, `{{steps.<key>.reject_reason}}`,
`{{run.root_issue}}`.

## Debugging a stuck run

```bash
multica workflow status <run-root-issue> --output json
multica issue get <run-root-issue> --output json     # progress block + comments
multica issue children <run-root-issue>              # the staged tree
```

A `needs_attention` run has stopped advancing on purpose (timeout, exhausted
retries/loops, rejected approval with no path, deadline). Read the reason in
`status_reason` / the root issue's comments, fix the cause, then
`multica workflow resume` or `multica workflow retry --step`.
