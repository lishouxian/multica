"use client";

import { useState } from "react";
import { toast } from "sonner";
import { useRunWorkflow } from "@multica/core/workflows";
import type { WorkflowDefinition } from "@multica/core/workflows";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { useT } from "../../i18n";

interface RunWorkflowDialogProps {
  workflow: WorkflowDefinition | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onStarted?: (rootIssueId: string) => void;
}

export function RunWorkflowDialog({
  workflow,
  open,
  onOpenChange,
  onStarted,
}: RunWorkflowDialogProps) {
  const { t } = useT("workflows");
  const runWorkflow = useRunWorkflow();
  const [vars, setVars] = useState<Record<string, string>>({});

  const varNames = workflow?.vars ?? [];

  function reset() {
    setVars({});
  }

  async function handleStart() {
    if (!workflow) return;
    try {
      const run = await runWorkflow.mutateAsync({ id: workflow.id, vars });
      toast.success(t(($) => $.toast.run_started));
      onOpenChange(false);
      reset();
      onStarted?.(run.root_issue_id);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.toast.run_failed));
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        onOpenChange(next);
        if (!next) reset();
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t(($) => $.run_dialog.title, { name: workflow?.name ?? "" })}
          </DialogTitle>
          <DialogDescription>{t(($) => $.run_dialog.description)}</DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3 py-1">
          {varNames.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              {t(($) => $.run_dialog.no_vars)}
            </p>
          ) : (
            varNames.map((name) => (
              <div key={name} className="flex flex-col gap-1.5">
                <Label htmlFor={`wf-var-${name}`}>{name}</Label>
                <Input
                  id={`wf-var-${name}`}
                  value={vars[name] ?? ""}
                  onChange={(e) =>
                    setVars((prev) => ({ ...prev, [name]: e.target.value }))
                  }
                  autoComplete="off"
                />
              </div>
            ))
          )}
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
          >
            {t(($) => $.run_dialog.cancel)}
          </Button>
          <Button
            type="button"
            size="sm"
            disabled={
              runWorkflow.isPending ||
              varNames.some((n) => !(vars[n] ?? "").trim())
            }
            onClick={handleStart}
          >
            {runWorkflow.isPending
              ? t(($) => $.run_dialog.starting)
              : t(($) => $.run_dialog.start)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
