import { describe, expect, it } from "vitest";
import {
  ListWorkflowsResponseSchema,
  EMPTY_LIST_WORKFLOWS_RESPONSE,
  WorkflowRunSchema,
  ListWorkflowRunsResponseSchema,
  EMPTY_LIST_WORKFLOW_RUNS_RESPONSE,
} from "./workflow-schemas";
import { parseWithFallback } from "./schema";

describe("workflow schemas degrade gracefully on drift", () => {
  it("falls back when the workflows list is malformed", () => {
    const out = parseWithFallback(
      { workflows: "not-an-array" },
      ListWorkflowsResponseSchema,
      EMPTY_LIST_WORKFLOWS_RESPONSE,
      { endpoint: "test" },
    );
    expect(out).toEqual(EMPTY_LIST_WORKFLOWS_RESPONSE);
  });

  it("keeps a run with an unknown status instead of dropping it", () => {
    // status is a server-driven enum; an unknown value must render as-is.
    const run = WorkflowRunSchema.parse({
      root_issue_id: "r1",
      workflow: "release",
      status: "some_future_status",
      steps: [{ key: "a", stage: 1, attempts: [] }],
    });
    expect(run.status).toBe("some_future_status");
    expect(run.steps).toHaveLength(1);
    // Missing fields default rather than throw.
    expect(run.frontier).toEqual([]);
    expect(run.vars).toEqual({});
  });

  it("defaults missing step/attempt fields", () => {
    const run = WorkflowRunSchema.parse({
      root_issue_id: "r1",
      steps: [{ key: "review", attempts: [{ issue_id: "i1" }] }],
    });
    expect(run.steps[0]?.stage).toBe(0);
    expect(run.steps[0]?.active).toBe(false);
    expect(run.steps[0]?.attempts[0]?.n).toBe(0);
  });

  it("falls back when the runs list is entirely malformed", () => {
    const out = parseWithFallback(
      null,
      ListWorkflowRunsResponseSchema,
      EMPTY_LIST_WORKFLOW_RUNS_RESPONSE,
      { endpoint: "test" },
    );
    expect(out).toEqual(EMPTY_LIST_WORKFLOW_RUNS_RESPONSE);
  });
});
