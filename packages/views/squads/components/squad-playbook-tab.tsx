"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { workspaceKeys } from "@multica/core/workspace/queries";
import type { PlaybookRun, Squad, SquadPlaybookResponse } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Loader2, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import { useT, useTimeAgo } from "../../i18n";
import { SquadPlaybookEditor } from "./squad-playbook-editor";

interface SquadPlaybookTabProps {
  squad: Squad;
  canManage: boolean;
  onDirtyChange: (dirty: boolean) => void;
}

function statusVariant(status: string): "default" | "secondary" | "destructive" | "outline" {
  if (status === "succeeded") return "default";
  if (status === "needs_attention" || status === "failed") return "destructive";
  if (status === "running" || status === "ready") return "secondary";
  return "outline";
}

export function SquadPlaybookTab({
  squad,
  canManage,
  onDirtyChange,
}: SquadPlaybookTabProps) {
  const { t } = useT("squads");
  const timeAgo = useTimeAgo();
  const wsId = useWorkspaceId();
  const queryClient = useQueryClient();
  const playbookKey = [...workspaceKeys.squads(wsId), squad.id, "playbook"] as const;
  const runsKey = [...workspaceKeys.squads(wsId), squad.id, "playbook-runs"] as const;

  const playbookQuery = useQuery({
    queryKey: playbookKey,
    queryFn: () => api.getSquadPlaybook(squad.id),
  });
  const runsQuery = useQuery({
    queryKey: runsKey,
    queryFn: () => api.listSquadPlaybookRuns(squad.id),
    refetchInterval: (query) => {
      const runs = query.state.data as PlaybookRun[] | undefined;
      return runs?.some((run) => run.status === "running" || run.status === "needs_attention")
        ? 3000
        : false;
    },
  });

  const saveMutation = useMutation({
    mutationFn: (definition: Record<string, unknown>) =>
      api.saveSquadPlaybook(squad.id, definition),
    onSuccess: (response) => {
      queryClient.setQueryData<SquadPlaybookResponse>(playbookKey, response);
      queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
      toast.success(t(($) => $.playbook_tab.saved));
    },
  });
  const disableMutation = useMutation({
    mutationFn: () => api.disableSquadPlaybook(squad.id),
    onSuccess: (response) => {
      queryClient.setQueryData<SquadPlaybookResponse>(playbookKey, response);
      queryClient.invalidateQueries({ queryKey: workspaceKeys.squads(wsId) });
      toast.success(t(($) => $.playbook_tab.disabled));
    },
  });
  const startMutation = useMutation({
    mutationFn: (rootIssueId: string) =>
      api.startSquadPlaybookRun(squad.id, rootIssueId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: runsKey });
      toast.success(t(($) => $.playbook_tab.started));
    },
  });

  if (playbookQuery.isLoading) {
    return (
      <div className="flex min-h-40 items-center justify-center">
        <Loader2 className="size-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  const response = playbookQuery.data;
  const editorKey = response?.definition
    ? `${response.definition.id}:${response.definition.version}`
    : "empty";

  return (
    <div className="space-y-8">
      <SquadPlaybookEditor
        key={editorKey}
        initialDefinition={response?.definition?.definition ?? null}
        canManage={canManage}
        active={response?.orchestration_mode === "playbook"}
        saving={saveMutation.isPending}
        disabling={disableMutation.isPending}
        starting={startMutation.isPending}
        onDirtyChange={onDirtyChange}
        onSave={(definition) => saveMutation.mutateAsync(definition).then(() => undefined)}
        onDisable={() => disableMutation.mutateAsync().then(() => undefined)}
        onStart={(rootIssueId) => startMutation.mutateAsync(rootIssueId).then(() => undefined)}
      />

      <section className="space-y-3 border-t pt-5" aria-labelledby="playbook-runs-heading">
        <div className="flex items-center justify-between gap-2">
          <div>
            <h3 id="playbook-runs-heading" className="text-sm font-medium">
              {t(($) => $.playbook_tab.runs_title)}
            </h3>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t(($) => $.playbook_tab.runs_description)}
            </p>
          </div>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            aria-label={t(($) => $.playbook_tab.refresh_runs)}
            onClick={() => void runsQuery.refetch()}
            disabled={runsQuery.isFetching}
          >
            <RefreshCw className={`size-3.5 ${runsQuery.isFetching ? "animate-spin" : ""}`} />
          </Button>
        </div>

        {runsQuery.data?.length ? (
          <div className="space-y-2">
            {runsQuery.data.map((run) => (
              <details key={run.id} className="rounded-md border px-3 py-2">
                <summary className="flex cursor-pointer list-none items-center gap-2 text-xs">
                  <Badge variant={statusVariant(run.status)}>{run.status}</Badge>
                  <span className="font-mono">{run.id.slice(0, 8)}</span>
                  <span className="min-w-0 flex-1 truncate text-muted-foreground">
                    {t(($) => $.playbook_tab.run_issue)} {run.root_issue_id.slice(0, 8)}
                  </span>
                  <span className="text-muted-foreground">{timeAgo(run.updated_at)}</span>
                </summary>
                <ol className="mt-3 space-y-2 border-t pt-3">
                  {run.nodes.map((node) => (
                    <li key={node.id} className="flex items-start gap-2 text-xs">
                      <Badge variant={statusVariant(node.status)}>{node.status}</Badge>
                      <div className="min-w-0 flex-1">
                        <p className="font-mono font-medium">{node.step_key}</p>
                        <p className="truncate text-muted-foreground">
                          {node.error || `${t(($) => $.playbook_tab.attempt)} ${node.attempt}`}
                        </p>
                      </div>
                    </li>
                  ))}
                </ol>
              </details>
            ))}
          </div>
        ) : (
          <p className="rounded-md border border-dashed px-3 py-6 text-center text-xs text-muted-foreground">
            {t(($) => $.playbook_tab.no_runs)}
          </p>
        )}
      </section>
    </div>
  );
}
