import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { workflowKeys } from "./queries";
import { useWorkspaceId } from "../hooks";

type RunControlAction = "pause" | "resume" | "cancel" | "eject" | "retry";

export function usePushWorkflow() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (source: string) => api.pushWorkflow(source),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workflowKeys.list(wsId) });
    },
  });
}

export function useArchiveWorkflow() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: (id: string) => api.archiveWorkflow(id),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workflowKeys.list(wsId) });
    },
  });
}

// useRunWorkflow starts a run. It awaits the server (a run creates a durable
// issue tree; we never optimistically fake one) then refreshes the runs list.
export function useRunWorkflow() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({ id, vars }: { id: string; vars: Record<string, string> }) =>
      api.runWorkflow(id, vars),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: workflowKeys.runs(wsId) });
    },
  });
}

// useWorkflowRunControl drives pause/resume/cancel/eject/retry. Each awaits the
// server and then invalidates the single run and the runs list — control ops
// change engine state that we must re-read, never predict.
export function useWorkflowRunControl() {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  return useMutation({
    mutationFn: ({
      rootIssueId,
      action,
      step,
    }: {
      rootIssueId: string;
      action: RunControlAction;
      step?: string;
    }) => api.workflowRunControl(rootIssueId, action, step ? { step } : undefined),
    onSettled: (_data, _err, variables) => {
      qc.invalidateQueries({ queryKey: workflowKeys.run(wsId, variables.rootIssueId) });
      qc.invalidateQueries({ queryKey: workflowKeys.runs(wsId) });
    },
  });
}
