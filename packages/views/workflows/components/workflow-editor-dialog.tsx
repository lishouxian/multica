"use client";

import { useEffect, useState } from "react";
import { toast } from "sonner";
import { CircleCheck, CircleX, LoaderCircle } from "lucide-react";
import { api } from "@multica/core/api";
import { usePushWorkflow } from "@multica/core/workflows";
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
import { Textarea } from "@multica/ui/components/ui/textarea";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../../i18n";

// Starter template for a new playbook. Deliberately minimal but complete:
// one work step with an output, one approval with a back-edge — the two
// concepts a first-time author needs to see.
const NEW_PLAYBOOK_TEMPLATE = `name: my-flow
vars: [goal]
steps:
  - key: work
    stage: 1
    assignee: "member:you@example.com"
    prompt: "Do the work for {{vars.goal}} and record the result."
    outputs:
      result: [ok, blocked]
    next:
      when: "steps.work.outputs.result"
      routes: { ok: review }
      default: needs_attention
  - key: review
    stage: 2
    type: approval
    assignee: "member:you@example.com"
    on_reject: { back_to: work, max_loops: 2 }
`;

type ValidationState =
  | { kind: "idle" }
  | { kind: "checking" }
  | { kind: "valid"; name: string; steps: number }
  | { kind: "invalid"; error: string };

interface WorkflowEditorDialogProps {
  // null = create a new playbook from the template; otherwise edit (the
  // upsert key is the YAML `name`, so renaming creates a new playbook).
  workflow: WorkflowDefinition | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSaved?: (saved: WorkflowDefinition) => void;
}

export function WorkflowEditorDialog({
  workflow,
  open,
  onOpenChange,
  onSaved,
}: WorkflowEditorDialogProps) {
  const { t } = useT("workflows");
  const push = usePushWorkflow();
  const [source, setSource] = useState("");
  const [validation, setValidation] = useState<ValidationState>({ kind: "idle" });

  // Seed the buffer each time the dialog opens; edits never write back into
  // cache objects.
  useEffect(() => {
    if (open) {
      setSource(workflow?.source ?? NEW_PLAYBOOK_TEMPLATE);
      setValidation({ kind: "idle" });
    }
  }, [open, workflow]);

  async function validate(): Promise<boolean> {
    setValidation({ kind: "checking" });
    try {
      const res = await api.validateWorkflow(source);
      if (res.valid === true) {
        setValidation({
          kind: "valid",
          name: res.name ?? "",
          steps: res.steps?.length ?? 0,
        });
        return true;
      }
      setValidation({ kind: "invalid", error: res.error ?? "invalid" });
      return false;
    } catch (err) {
      setValidation({
        kind: "invalid",
        error: err instanceof Error ? err.message : String(err),
      });
      return false;
    }
  }

  async function save() {
    if (!(await validate())) return;
    try {
      const saved = await push.mutateAsync(source);
      toast.success(t(($) => $.editor.saved));
      onOpenChange(false);
      onSaved?.(saved);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : t(($) => $.editor.save_failed));
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>
            {workflow
              ? t(($) => $.editor.edit_title, { name: workflow.name })
              : t(($) => $.editor.create_title)}
          </DialogTitle>
          <DialogDescription>
            {workflow ? t(($) => $.editor.rename_hint) : t(($) => $.editor.description)}
          </DialogDescription>
        </DialogHeader>

        <div className="flex min-h-0 flex-1 flex-col gap-2">
          <Textarea
            value={source}
            onChange={(e) => {
              setSource(e.target.value);
              setValidation({ kind: "idle" });
            }}
            spellCheck={false}
            className="min-h-[320px] flex-1 resize-none font-mono text-xs leading-relaxed"
          />
          {validation.kind !== "idle" ? (
            <div
              className={cn(
                "flex items-start gap-1.5 rounded-md border px-2.5 py-1.5 text-xs",
                validation.kind === "invalid" && "border-destructive/50 text-destructive",
                validation.kind === "valid" && "border-success/50 text-success",
                validation.kind === "checking" && "text-muted-foreground",
              )}
              role={validation.kind === "invalid" ? "alert" : undefined}
            >
              {validation.kind === "checking" ? (
                <LoaderCircle className="mt-0.5 size-3.5 shrink-0 animate-spin" aria-hidden />
              ) : validation.kind === "valid" ? (
                <CircleCheck className="mt-0.5 size-3.5 shrink-0" aria-hidden />
              ) : (
                <CircleX className="mt-0.5 size-3.5 shrink-0" aria-hidden />
              )}
              <span className="min-w-0 break-words">
                {validation.kind === "checking"
                  ? t(($) => $.editor.checking)
                  : validation.kind === "valid"
                    ? t(($) => $.editor.valid, {
                        name: validation.name,
                        count: validation.steps,
                      })
                    : validation.error}
              </span>
            </div>
          ) : null}
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => onOpenChange(false)}
          >
            {t(($) => $.editor.cancel)}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={validation.kind === "checking" || source.trim() === ""}
            onClick={() => void validate()}
          >
            {t(($) => $.editor.validate)}
          </Button>
          <Button
            type="button"
            size="sm"
            disabled={push.isPending || validation.kind === "checking" || source.trim() === ""}
            onClick={() => void save()}
          >
            {push.isPending ? t(($) => $.editor.saving) : t(($) => $.editor.save)}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
