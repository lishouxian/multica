"use client";

import type { PlaybookRun } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { ExternalLink, Workflow } from "lucide-react";
import { AppLink } from "../../navigation";
import { useT, useTimeAgo } from "../../i18n";
import { PlaybookGraph } from "../../playbooks/playbook-graph";

interface IssuePlaybookRunCardProps {
  run: PlaybookRun;
  agentNames: ReadonlyMap<string, string>;
}

function statusVariant(status: string): "default" | "secondary" | "destructive" | "outline" {
  if (status === "succeeded") return "default";
  if (status === "failed" || status === "needs_attention") return "destructive";
  if (status === "running") return "secondary";
  return "outline";
}

export function IssuePlaybookRunCard({ run, agentNames }: IssuePlaybookRunCardProps) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const paths = useWorkspacePaths();
  const completed = run.nodes.filter((node) =>
    node.status === "succeeded" || node.status === "skipped" || node.status === "cancelled"
  ).length;

  return (
    <section
      className="mt-5 space-y-4 rounded-lg border bg-card/40 p-4"
      aria-labelledby="issue-playbook-run-heading"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-2.5">
          <div className="mt-0.5 rounded-md bg-primary/10 p-1.5 text-primary">
            <Workflow className="size-4" aria-hidden="true" />
          </div>
          <div className="min-w-0">
            <h2 id="issue-playbook-run-heading" className="text-sm font-medium">
              {t(($) => $.detail.playbook_run_title)}
            </h2>
            <p className="mt-0.5 text-xs text-muted-foreground">
              <span className="font-mono">#{run.id.slice(0, 8)}</span>
              <span className="px-1.5">·</span>
              {t(($) => $.detail.playbook_run_updated, { time: timeAgo(run.updated_at) })}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <span className="text-xs tabular-nums text-muted-foreground">
            {completed}/{run.nodes.length}
          </span>
          <Badge variant={statusVariant(run.status)}>{run.status}</Badge>
        </div>
      </div>

      <PlaybookGraph
        definition={run.definition_snapshot}
        nodes={run.nodes}
        agentNames={agentNames}
        issueHref={paths.issueDetail}
      />

      <div className="flex justify-end border-t pt-3">
        <AppLink
          href={paths.squadDetail(run.squad_id)}
          className="inline-flex items-center gap-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
        >
          {t(($) => $.detail.open_playbook)}
          <ExternalLink className="size-3" aria-hidden="true" />
        </AppLink>
      </div>
    </section>
  );
}
