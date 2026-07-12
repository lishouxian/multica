"use client";

import type { PlaybookNodeRun } from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { cn } from "@multica/ui/lib/utils";
import { ArrowRight, Bot, GitBranch, RotateCcw } from "lucide-react";
import { AppLink } from "../navigation";

export interface PlaybookGraphStep {
  key: string;
  title: string;
  agentId: string;
  dependsOn: string[];
  inputNames: string[];
  outputNames: string[];
  maxAttempts: number;
  isStart: boolean;
  transitions: Array<{ to: string; label: string }>;
}

interface PlaybookGraphProps {
  definition: Record<string, unknown>;
  nodes?: PlaybookNodeRun[];
  agentNames?: ReadonlyMap<string, string>;
  issueHref?: (issueId: string) => string;
  className?: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function parsePlaybookGraphSteps(
  definition: Record<string, unknown>,
): PlaybookGraphStep[] {
  if (!Array.isArray(definition.steps)) return [];
  return definition.steps.flatMap((rawStep) => {
    if (!isRecord(rawStep) || typeof rawStep.key !== "string") return [];
    const outputSchema = isRecord(rawStep.output_schema) ? rawStep.output_schema : {};
    const outputProperties = isRecord(outputSchema.properties) ? outputSchema.properties : {};
    const input = isRecord(rawStep.input) ? rawStep.input : {};
    const transitions = Array.isArray(rawStep.transitions)
      ? rawStep.transitions.flatMap((rawTransition) => {
          if (!isRecord(rawTransition)) return [];
          const when = isRecord(rawTransition.when) ? rawTransition.when : null;
          const equals = when?.equals;
          const label = equals === undefined
            ? "default"
            : typeof equals === "string" || typeof equals === "number" || typeof equals === "boolean"
              ? String(equals)
              : JSON.stringify(equals);
          if (rawTransition.end === true) return [{ to: "END", label }];
          return typeof rawTransition.to === "string" ? [{ to: rawTransition.to, label }] : [];
        })
      : [];
    return [{
      key: rawStep.key,
      title: typeof rawStep.title === "string" ? rawStep.title : rawStep.key,
      agentId: typeof rawStep.agent_id === "string" ? rawStep.agent_id : "",
      dependsOn: Array.isArray(rawStep.depends_on)
        ? rawStep.depends_on.filter((value): value is string => typeof value === "string")
        : [],
      inputNames: Object.keys(input),
      outputNames: Object.keys(outputProperties),
      maxAttempts: typeof rawStep.max_attempts === "number" && rawStep.max_attempts > 0
        ? rawStep.max_attempts
        : 2,
      isStart: definition.start === rawStep.key,
      transitions,
    }];
  });
}

function groupByDepth(steps: PlaybookGraphStep[]): PlaybookGraphStep[][] {
  if (steps.some((step) => step.transitions.length > 0)) {
    return steps.map((step) => [step]);
  }
  const byKey = new Map(steps.map((step) => [step.key, step]));
  const depths = new Map<string, number>();
  const visit = (key: string, visiting: Set<string>): number => {
    const cached = depths.get(key);
    if (cached !== undefined) return cached;
    if (visiting.has(key)) return 0;
    const step = byKey.get(key);
    if (!step) return 0;
    const nextVisiting = new Set(visiting).add(key);
    const depth = step.dependsOn.length === 0
      ? 0
      : Math.max(...step.dependsOn.map((dependency) => visit(dependency, nextVisiting))) + 1;
    depths.set(key, depth);
    return depth;
  };
  for (const step of steps) visit(step.key, new Set());
  const groups: PlaybookGraphStep[][] = [];
  for (const step of steps) {
    const depth = depths.get(step.key) ?? 0;
    (groups[depth] ??= []).push(step);
  }
  return groups.filter(Boolean);
}

function statusVariant(status: string | undefined): "default" | "secondary" | "destructive" | "outline" {
  if (status === "succeeded") return "default";
  if (status === "failed" || status === "needs_attention") return "destructive";
  if (status === "running" || status === "ready") return "secondary";
  return "outline";
}

export function PlaybookGraph({
  definition,
  nodes = [],
  agentNames,
  issueHref,
  className,
}: PlaybookGraphProps) {
  const steps = parsePlaybookGraphSteps(definition);
  const levels = groupByDepth(steps);
  const nodesByKey = new Map(nodes.map((node) => [node.step_key, node]));

  if (steps.length === 0) return null;

  return (
    <div className={cn("overflow-x-auto rounded-lg border bg-muted/20 p-4", className)}>
      <div className="flex min-w-max items-center gap-3" role="list">
        {levels.map((level, levelIndex) => (
          <div key={level.map((step) => step.key).join(":")} className="contents">
            {levelIndex > 0 && (
              <ArrowRight className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
            )}
            <div className="flex w-56 shrink-0 flex-col gap-2">
              {level.map((step) => {
                const node = nodesByKey.get(step.key);
                const card = (
                  <div
                    className={cn(
                      "rounded-md border bg-background p-3 shadow-sm transition-colors",
                      node?.status === "running" && "border-primary/60 bg-primary/5",
                      node?.status === "needs_attention" && "border-destructive/60 bg-destructive/5",
                      node?.issue_id && "hover:border-foreground/30 hover:bg-accent/40",
                    )}
                  >
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="flex items-center gap-1.5">
                          <p className="truncate text-sm font-medium">{step.title}</p>
                          {step.isStart && <Badge variant="outline">START</Badge>}
                        </div>
                        <p className="font-mono text-[11px] text-muted-foreground">{step.key}</p>
                      </div>
                      {node && <Badge variant={statusVariant(node.status)}>{node.status}</Badge>}
                    </div>
                    {step.dependsOn.length > 0 && (
                      <div className="mt-2 flex items-center gap-1 text-[11px] text-muted-foreground">
                        <GitBranch className="size-3" aria-hidden="true" />
                        <span className="truncate">{step.dependsOn.join(" + ")}</span>
                      </div>
                    )}
                    {step.transitions.length > 0 && (
                      <div className="mt-2 space-y-1 border-t pt-2 text-[11px] text-muted-foreground">
                        {step.transitions.map((transition, index) => (
                          <div key={`${transition.label}:${transition.to}:${index}`} className="flex items-center gap-1">
                            <RotateCcw className="size-3 shrink-0" aria-hidden="true" />
                            <span className="truncate">{transition.label} → {transition.to}</span>
                          </div>
                        ))}
                      </div>
                    )}
                    <div className="mt-2 flex items-center gap-1.5 text-xs text-muted-foreground">
                      <Bot className="size-3.5 shrink-0" aria-hidden="true" />
                      <span className="truncate">
                        {agentNames?.get(step.agentId) ?? step.agentId.slice(0, 8)}
                      </span>
                    </div>
                    {step.maxAttempts > 1 && (
                      <p className="mt-1 text-[11px] text-muted-foreground">
                        {node ? `${node.attempt}/${step.maxAttempts}` : `≤ ${step.maxAttempts}`}
                      </p>
                    )}
                    {(step.inputNames.length > 0 || step.outputNames.length > 0) && (
                      <div className="mt-2 truncate border-t pt-2 font-mono text-[10px] text-muted-foreground">
                        {step.inputNames.join(", ") || "∅"} → {step.outputNames.join(", ") || "∅"}
                      </div>
                    )}
                  </div>
                );
                return node?.issue_id && issueHref ? (
                  <AppLink
                    key={step.key}
                    href={issueHref(node.issue_id)}
                    role="listitem"
                    className="rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {card}
                  </AppLink>
                ) : (
                  <div key={step.key} role="listitem">{card}</div>
                );
              })}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
