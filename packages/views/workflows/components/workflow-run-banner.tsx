"use client";

import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { CheckCircle2, CircleDashed, LoaderCircle, XCircle } from "lucide-react";
import { workflowRunOptions } from "@multica/core/workflows/queries";
import { useWorkflowRunControl } from "@multica/core/workflows";
import type { WorkflowRun, WorkflowRunStep } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { WorkflowStatusBadge } from "./workflow-status-badge";

// Live refresh cadence for an open run. The engine advances on a ~30s server
// tick, so polling a touch faster keeps the banner close to real time without
// being wasteful.
const RUN_POLL_MS = 15_000;

interface WorkflowRunBannerProps {
  issueId: string;
  // The issue's metadata; the banner renders only when this issue is a run
  // root (carries the workflow_state key). Kept as a prop so the host issue
  // detail decides placement without this component fetching the issue.
  metadata: Record<string, unknown> | undefined;
}

export function WorkflowRunBanner({ issueId, metadata }: WorkflowRunBannerProps) {
  const isRunRoot = metadata != null && "workflow_state" in metadata;
  const wsId = useWorkspaceId();
  const run = useQuery(
    workflowRunOptions(wsId, issueId, {
      enabled: isRunRoot,
      refetchInterval: RUN_POLL_MS,
    }),
  );
  if (!isRunRoot || !run.data) return null;
  return <WorkflowRunBannerBody run={run.data} />;
}

function WorkflowRunBannerBody({ run }: { run: WorkflowRun }) {
  const { t } = useT("workflows");
  const paths = useWorkspacePaths();
  const nav = useNavigation();
  const control = useWorkflowRunControl();

  const isActive = run.status === "running" || run.status === "paused";
  const stagesTotal = new Set(run.steps.map((s) => s.stage)).size;
  const currentStage =
    run.steps.find((s) => s.active)?.stage ??
    (run.status === "done" ? stagesTotal : 0);

  async function act(action: "pause" | "resume" | "cancel" | "eject") {
    try {
      await control.mutateAsync({ rootIssueId: run.root_issue_id, action });
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.toast.control_failed));
    }
  }

  return (
    <div className="rounded-lg border bg-muted/30 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium">
          {t(($) => $.banner.title)} · {run.workflow}
        </span>
        <WorkflowStatusBadge status={run.status} />
        {stagesTotal > 0 ? (
          <span className="text-xs text-muted-foreground">
            {t(($) => $.banner.step_of, {
              current: currentStage,
              total: stagesTotal,
            })}
          </span>
        ) : null}
        <div className="ml-auto flex items-center gap-1.5">
          {run.status === "running" ? (
            <Button size="sm" variant="ghost" disabled={control.isPending} onClick={() => act("pause")}>
              {t(($) => $.banner.pause)}
            </Button>
          ) : null}
          {run.status === "paused" || run.status === "needs_attention" ? (
            <Button size="sm" variant="ghost" disabled={control.isPending} onClick={() => act("resume")}>
              {t(($) => $.banner.resume)}
            </Button>
          ) : null}
          {isActive || run.status === "needs_attention" ? (
            <Button size="sm" variant="ghost" disabled={control.isPending} onClick={() => act("eject")}>
              {t(($) => $.banner.eject)}
            </Button>
          ) : null}
        </div>
      </div>

      {run.status_reason ? (
        <p className="mt-1.5 text-xs text-muted-foreground">
          {t(($) => $.banner.reason)}: {run.status_reason}
        </p>
      ) : null}

      {/* Step chips: click a chip to jump to that step's latest attempt. */}
      <div className="mt-2.5 flex flex-wrap gap-1.5">
        {run.steps.map((step) => (
          <StepChip
            key={step.key}
            step={step}
            onOpen={(issueId) => nav.push(paths.issueDetail(issueId))}
          />
        ))}
      </div>
    </div>
  );
}

function StepChip({
  step,
  onOpen,
}: {
  step: WorkflowRunStep;
  onOpen: (issueId: string) => void;
}) {
  const latest = step.attempts[step.attempts.length - 1];
  const status = latest?.issue_status ?? "";
  const isApproval = step.type === "approval";

  let Icon = CircleDashed;
  let tone = "text-muted-foreground";
  if (status === "done") {
    Icon = CheckCircle2;
    tone = "text-success";
  } else if (status === "cancelled") {
    Icon = XCircle;
    tone = "text-destructive";
  } else if (step.active) {
    Icon = LoaderCircle;
    tone = "text-brand";
  }

  const clickable = latest != null;
  return (
    <button
      type="button"
      disabled={!clickable}
      onClick={() => latest && onOpen(latest.issue_id)}
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs",
        clickable ? "hover:bg-accent" : "cursor-default opacity-70",
        step.active && "border-brand/50",
      )}
      title={isApproval ? `${step.key} (approval)` : step.key}
    >
      <Icon className={cn("size-3", tone, step.active && "animate-spin")} aria-hidden />
      <span className="truncate max-w-[10rem]">{step.key}</span>
    </button>
  );
}
