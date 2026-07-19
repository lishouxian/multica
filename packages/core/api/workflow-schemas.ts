import { z } from "zod";

// Workflow API schemas (feat/workflow-v0). Every response the UI consumes is
// parsed through these with parseWithFallback so a backend that drifts (new
// status value, extra field, missing field) degrades instead of white-screening
// — the same discipline the rest of the API client follows.

export const WorkflowStepInfoSchema = z.object({
  key: z.string().default(""),
  stage: z.number().default(0),
  type: z.string().default(""),
  assignee: z.string().default(""),
  title: z.string().default(""),
}).loose();
export type WorkflowStepInfo = z.infer<typeof WorkflowStepInfoSchema>;

export const WorkflowDefinitionSchema = z.object({
  id: z.string().default(""),
  name: z.string().default(""),
  source: z.string().default(""),
  vars: z.array(z.string()).default([]),
  steps: z.array(WorkflowStepInfoSchema).default([]),
  created_by: z.string().default(""),
  created_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();
export type WorkflowDefinition = z.infer<typeof WorkflowDefinitionSchema>;

export const ListWorkflowsResponseSchema = z.object({
  workflows: z.array(WorkflowDefinitionSchema).default([]),
}).loose();
export type ListWorkflowsResponse = z.infer<typeof ListWorkflowsResponseSchema>;
export const EMPTY_LIST_WORKFLOWS_RESPONSE: ListWorkflowsResponse = { workflows: [] };

export const ValidateWorkflowResponseSchema = z.object({
  valid: z.boolean().default(false),
  error: z.string().default(""),
  name: z.string().default(""),
  steps: z.array(WorkflowStepInfoSchema).default([]),
}).loose();
export type ValidateWorkflowResponse = z.infer<typeof ValidateWorkflowResponseSchema>;

export const WorkflowRunAttemptSchema = z.object({
  issue_id: z.string().default(""),
  identifier: z.string().default(""),
  n: z.number().default(0),
  issue_status: z.string().default(""),
  created_at: z.string().default(""),
}).loose();
export type WorkflowRunAttempt = z.infer<typeof WorkflowRunAttemptSchema>;

export const WorkflowRunStepSchema = z.object({
  key: z.string().default(""),
  stage: z.number().default(0),
  type: z.string().default(""),
  assignee: z.string().default(""),
  active: z.boolean().default(false),
  loops_used: z.number().default(0),
  attempts: z.array(WorkflowRunAttemptSchema).default([]),
}).loose();
export type WorkflowRunStep = z.infer<typeof WorkflowRunStepSchema>;

// Run status is a server-driven enum; keep it a plain string on the client so
// an unknown value renders as-is instead of throwing (a default branch, in the
// same spirit as the engine's routing).
export const WorkflowRunSchema = z.object({
  root_issue_id: z.string().default(""),
  root_identifier: z.string().default(""),
  root_title: z.string().default(""),
  workflow: z.string().default(""),
  definition_id: z.string().default(""),
  def_sha: z.string().default(""),
  status: z.string().default(""),
  status_reason: z.string().default(""),
  vars: z.record(z.string(), z.string()).default({}),
  frontier: z.array(z.string()).default([]),
  steps: z.array(WorkflowRunStepSchema).default([]),
  started_at: z.string().default(""),
  updated_at: z.string().default(""),
}).loose();
export type WorkflowRun = z.infer<typeof WorkflowRunSchema>;

export const ListWorkflowRunsResponseSchema = z.object({
  runs: z.array(WorkflowRunSchema).default([]),
}).loose();
export type ListWorkflowRunsResponse = z.infer<typeof ListWorkflowRunsResponseSchema>;
export const EMPTY_LIST_WORKFLOW_RUNS_RESPONSE: ListWorkflowRunsResponse = { runs: [] };
