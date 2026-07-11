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
      },
      {
        key: "review",
        title: "Review",
        agentId: "agent-2",
        dependsOn: ["triage"],
        inputNames: ["summary"],
        outputNames: ["verdict"],
        maxAttempts: 2,
      },
    ]);
  });

  it("ignores malformed steps instead of crashing an installed client", () => {
    expect(parsePlaybookGraphSteps({ steps: [null, { title: "Missing key" }] })).toEqual([]);
  });
});
