"use client";

import type { PlaybookRun } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Activity, Clock3, ExternalLink, RotateCcw, Workflow } from "lucide-react";
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

interface PlaybookStepSummary {
  key: string;
  title: string;
  max_attempts?: number;
}

interface PlaybookTraceEntry {
  sequence: number;
  from: string;
  to?: string;
  end?: boolean;
}

function readSteps(run: PlaybookRun): PlaybookStepSummary[] {
  const steps = run.definition_snapshot.steps;
  if (!Array.isArray(steps)) return [];
  return steps.filter((step): step is PlaybookStepSummary =>
    typeof step === "object" && step !== null && typeof step.key === "string" && typeof step.title === "string"
  );
}

function readTrace(run: PlaybookRun): PlaybookTraceEntry[] {
  const runtime = run.context._playbook;
  if (typeof runtime !== "object" || runtime === null) return [];
  const trace = (runtime as Record<string, unknown>).trace;
  if (!Array.isArray(trace)) return [];
  return trace.filter((entry): entry is PlaybookTraceEntry =>
    typeof entry === "object" && entry !== null &&
    typeof entry.sequence === "number" && typeof entry.from === "string"
  );
}

function formatDuration(start: string, end: string): string {
  const seconds = Math.max(0, Math.round((Date.parse(end) - Date.parse(start)) / 1000));
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
}

export function IssuePlaybookRunCard({ run, agentNames }: IssuePlaybookRunCardProps) {
  const { t } = useT("issues");
  const timeAgo = useTimeAgo();
  const paths = useWorkspacePaths();
  const steps = readSteps(run);
  const stepByKey = new Map(steps.map((step) => [step.key, step]));
  const trace = readTrace(run);
  const completed = run.nodes.filter((node) =>
    node.status === "succeeded" || node.status === "skipped" || node.status === "cancelled"
  ).length;
  const retries = run.nodes.reduce((total, node) => total + Math.max(0, node.attempt - 1), 0);
  const currentNode = run.nodes.find((node) => node.status === "needs_attention")
    ?? run.nodes.find((node) => node.status === "running" || node.status === "ready");
  const currentStep = currentNode ? stepByKey.get(currentNode.step_key) : undefined;
  const currentAgent = currentNode ? agentNames.get(currentNode.agent_id) : undefined;
  const executionPath = trace.flatMap((entry, index) => {
    const values = index === 0 ? [entry.from] : [];
    values.push(entry.end ? "END" : (entry.to ?? "?"));
    return values;
  });
  const duration = formatDuration(run.created_at, run.completed_at ?? run.updated_at);

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
          <Badge variant={statusVariant(run.status)}>
            {t(($) => $.detail.playbook_status, { status: run.status })}
          </Badge>
        </div>
      </div>

      <div className="grid gap-3 rounded-md border bg-background/60 p-3 sm:grid-cols-[minmax(0,1.7fr)_repeat(3,minmax(0,1fr))]">
        <div className="min-w-0">
          <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            {t(($) => $.detail.playbook_current_step)}
          </p>
          <p className="mt-1 truncate text-sm font-medium">
            {currentStep?.title ?? (run.status === "succeeded"
              ? t(($) => $.detail.playbook_completed)
              : t(($) => $.detail.playbook_waiting))}
          </p>
          {currentNode && (
            <p className="mt-0.5 truncate text-xs text-muted-foreground">
              {currentAgent ?? currentNode.agent_id.slice(0, 8)}
              <span className="px-1">·</span>
              {t(($) => $.detail.playbook_attempt, {
                current: currentNode.attempt,
                max: currentStep?.max_attempts ?? 2,
              })}
            </p>
          )}
        </div>
        <div>
          <p className="flex items-center gap-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            <Activity className="size-3" aria-hidden="true" />
            {t(($) => $.detail.playbook_transitions)}
          </p>
          <p className="mt-1 font-mono text-sm font-medium tabular-nums">{trace.length}</p>
        </div>
        <div>
          <p className="flex items-center gap-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            <RotateCcw className="size-3" aria-hidden="true" />
            {t(($) => $.detail.playbook_retries)}
          </p>
          <p className="mt-1 font-mono text-sm font-medium tabular-nums">{retries}</p>
        </div>
        <div>
          <p className="flex items-center gap-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            <Clock3 className="size-3" aria-hidden="true" />
            {t(($) => $.detail.playbook_duration)}
          </p>
          <p className="mt-1 font-mono text-sm font-medium tabular-nums">{duration}</p>
        </div>
      </div>

      {executionPath.length > 0 && (
        <div className="space-y-1.5">
          <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
            {t(($) => $.detail.playbook_execution_path)}
          </p>
          <div className="flex flex-wrap items-center gap-1 text-xs">
            {executionPath.map((key, index) => (
              <span key={`${index}-${key}`} className="contents">
                {index > 0 && <span className="text-muted-foreground" aria-hidden="true">→</span>}
                <span className="rounded bg-muted px-1.5 py-0.5 font-medium">
                  {key === "END" ? t(($) => $.detail.playbook_completed) : (stepByKey.get(key)?.title ?? key)}
                </span>
              </span>
            ))}
          </div>
        </div>
      )}

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
