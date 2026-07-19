import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";

// Workflow query keys are workspace-scoped (wsId first) so a workspace switch
// cannot serve another workspace's cached definitions/runs.
export const workflowKeys = {
  all: (wsId: string) => ["workflows", wsId] as const,
  list: (wsId: string) => [...workflowKeys.all(wsId), "list"] as const,
  detail: (wsId: string, id: string) =>
    [...workflowKeys.all(wsId), "detail", id] as const,
  runs: (wsId: string) => [...workflowKeys.all(wsId), "runs"] as const,
  run: (wsId: string, rootIssueId: string) =>
    [...workflowKeys.all(wsId), "runs", rootIssueId] as const,
};

export function workflowListOptions(wsId: string) {
  return queryOptions({
    queryKey: workflowKeys.list(wsId),
    queryFn: () => api.listWorkflows(),
    select: (data) => data.workflows,
  });
}

export function workflowDetailOptions(
  wsId: string,
  id: string,
  options?: { enabled?: boolean },
) {
  return queryOptions({
    queryKey: workflowKeys.detail(wsId, id),
    queryFn: () => api.getWorkflow(id),
    enabled: (options?.enabled ?? true) && id !== "",
  });
}

export function workflowRunsOptions(wsId: string) {
  return queryOptions({
    queryKey: workflowKeys.runs(wsId),
    queryFn: () => api.listWorkflowRuns(),
    select: (data) => data.runs,
  });
}

export function workflowRunOptions(
  wsId: string,
  rootIssueId: string,
  options?: { enabled?: boolean; refetchInterval?: number },
) {
  return queryOptions({
    queryKey: workflowKeys.run(wsId, rootIssueId),
    queryFn: () => api.getWorkflowRun(rootIssueId),
    enabled: (options?.enabled ?? true) && rootIssueId !== "",
    refetchInterval: options?.refetchInterval,
  });
}
