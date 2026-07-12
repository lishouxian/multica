import { describe, expect, it } from "vitest";
import { parsePlaybookGraphSteps } from "./playbook-graph";

describe("parsePlaybookGraphSteps", () => {
  it("projects dependencies and handoff field names from a saved definition", () => {
    expect(parsePlaybookGraphSteps({
      version: 1,
      steps: [
        {
          key: "triage",
          title: "Triage",
          agent_id: "agent-1",
          output_schema: { properties: { outcome: {}, summary: {} } },
        },
        {
          key: "review",
          title: "Review",
          agent_id: "agent-2",
          depends_on: ["triage"],
          input: { summary: { from: "triage.summary" } },
          output_schema: { properties: { verdict: {} } },
        },
      ],
    })).toEqual([
      {
        key: "triage",
        title: "Triage",
        agentId: "agent-1",
        dependsOn: [],
        inputNames: [],
        outputNames: ["outcome", "summary"],
        maxAttempts: 2,
        isStart: false,
        transitions: [],
      },
      {
        key: "review",
        title: "Review",
        agentId: "agent-2",
        dependsOn: ["triage"],
        inputNames: ["summary"],
        outputNames: ["verdict"],
        maxAttempts: 2,
        isStart: false,
        transitions: [],
      },
    ]);
  });

  it("projects state-machine start and conditional back-edges", () => {
    expect(parsePlaybookGraphSteps({
      version: 1,
      start: "code",
      steps: [{
        key: "code",
        title: "Code",
        agent_id: "agent-1",
        transitions: [
          { to: "design", when: { field: "outcome", equals: "design_issue" } },
          { to: "unit", when: { field: "outcome", equals: "done" } },
        ],
        output_schema: { properties: { outcome: {} } },
      }],
    })).toEqual([expect.objectContaining({
      key: "code",
      isStart: true,
      transitions: [
        { to: "design", label: "design_issue" },
        { to: "unit", label: "done" },
      ],
    })]);
  });

  it("ignores malformed steps instead of crashing an installed client", () => {
    expect(parsePlaybookGraphSteps({ steps: [null, { title: "Missing key" }] })).toEqual([]);
  });
});
