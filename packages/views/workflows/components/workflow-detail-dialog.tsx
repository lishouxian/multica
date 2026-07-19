"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, Copy, Pencil, Play } from "lucide-react";
import { workflowDetailOptions } from "@multica/core/workflows/queries";
import type { WorkflowDefinition } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Badge } from "@multica/ui/components/ui/badge";
import { useT } from "../../i18n";

interface WorkflowDetailDialogProps {
  // The list row the user clicked; the dialog refetches the full definition
  // (with source) so a stale list row never shows outdated YAML.
  workflow: WorkflowDefinition | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onRun?: (workflow: WorkflowDefinition) => void;
  onEdit?: (workflow: WorkflowDefinition) => void;
}

export function WorkflowDetailDialog({
  workflow,
  open,
  onOpenChange,
  onRun,
  onEdit,
}: WorkflowDetailDialogProps) {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const detail = useQuery(
    workflowDetailOptions(wsId, workflow?.id ?? "", { enabled: open }),
  );
  const [copied, setCopied] = useState(false);

  // Prefer the fresh detail; fall back to the list row while loading so the
  // dialog opens instantly with steps already known.
  const def = detail.data ?? workflow;
  if (!workflow) return null;

  async function copySource() {
    if (!def?.source) return;
    try {
      await navigator.clipboard.writeText(def.source);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error(t(($) => $.detail.copy_failed));
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next);
        if (!next) setCopied(false);
      }}
    >
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{def?.name ?? workflow.name}</DialogTitle>
          <DialogDescription>
            {t(($) => $.detail.description)}
          </DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto">
          {/* Steps summary */}
          <div className="flex flex-col gap-1.5">
            <span className="text-xs font-medium text-muted-foreground">
              {t(($) => $.detail.steps)}
            </span>
            <div className="flex flex-wrap items-center gap-1.5">
              {(def?.steps ?? []).map((step) => (
                <span
                  key={step.key}
                  className="inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs"
                >
                  <span className="font-mono text-muted-foreground">
                    {step.stage}
                  </span>
                  <span>{step.key}</span>
                  {step.type === "approval" ? (
                    <Badge variant="secondary" className="px-1 py-0 text-[10px]">
                      {t(($) => $.detail.approval)}
                    </Badge>
                  ) : null}
                </span>
              ))}
            </div>
          </div>

          {/* YAML source */}
          <div className="flex min-h-0 flex-1 flex-col gap-1.5">
            <div className="flex items-center justify-between">
              <span className="text-xs font-medium text-muted-foreground">
                {t(($) => $.detail.source)}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-7 gap-1.5 px-2 text-xs"
                onClick={copySource}
                disabled={!def?.source}
              >
                {copied ? (
                  <Check className="size-3.5" aria-hidden />
                ) : (
                  <Copy className="size-3.5" aria-hidden />
                )}
                {copied ? t(($) => $.detail.copied) : t(($) => $.detail.copy)}
              </Button>
            </div>
            <pre className="min-h-0 flex-1 overflow-auto rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed">
              {def?.source || t(($) => $.detail.loading)}
            </pre>
          </div>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
          >
            {t(($) => $.detail.close)}
          </Button>
          {onEdit ? (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                onOpenChange(false);
                // Hand the freshest source to the editor so an edit opened
                // from a stale list row still starts from what the server has.
                onEdit(def ?? workflow);
              }}
            >
              <Pencil className="size-3.5" aria-hidden />
              {t(($) => $.detail.edit)}
            </Button>
          ) : null}
          {onRun ? (
            <Button
              type="button"
              size="sm"
              onClick={() => {
                onOpenChange(false);
                onRun(workflow);
              }}
            >
              <Play className="size-3.5" aria-hidden />
              {t(($) => $.definitions.run)}
            </Button>
          ) : null}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
