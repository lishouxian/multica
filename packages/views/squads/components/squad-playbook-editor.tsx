"use client";

import { useState } from "react";
import { Loader2, Play, Power, Save } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";

interface SquadPlaybookEditorProps {
  initialDefinition: Record<string, unknown> | null;
  canManage: boolean;
  active: boolean;
  saving: boolean;
  disabling: boolean;
  starting: boolean;
  onDirtyChange: (dirty: boolean) => void;
  onSave: (definition: Record<string, unknown>) => Promise<void>;
  onDisable: () => Promise<void>;
  onStart: (rootIssueId: string) => Promise<void>;
}

const EMPTY_PLAYBOOK = {
  version: 1,
  steps: [],
};

export function SquadPlaybookEditor({
  initialDefinition,
  canManage,
  active,
  saving,
  disabling,
  starting,
  onDirtyChange,
  onSave,
  onDisable,
  onStart,
}: SquadPlaybookEditorProps) {
  const { t } = useT("squads");
  const initialDraft = JSON.stringify(initialDefinition ?? EMPTY_PLAYBOOK, null, 2);
  const [draft, setDraft] = useState(initialDraft);
  const [rootIssueId, setRootIssueId] = useState("");
  const [error, setError] = useState<string | null>(null);
  const dirty = draft !== initialDraft;

  const updateDraft = (value: string) => {
    setDraft(value);
    setError(null);
    onDirtyChange(value !== initialDraft);
  };

  const save = async () => {
    let definition: unknown;
    try {
      definition = JSON.parse(draft);
    } catch {
      setError(t(($) => $.playbook_tab.invalid_json));
      return;
    }
    if (!definition || typeof definition !== "object" || Array.isArray(definition)) {
      setError(t(($) => $.playbook_tab.invalid_definition));
      return;
    }
    try {
      await onSave(definition as Record<string, unknown>);
      onDirtyChange(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t(($) => $.playbook_tab.save_failed));
    }
  };

  const start = async () => {
    if (!rootIssueId.trim()) return;
    setError(null);
    try {
      await onStart(rootIssueId.trim());
      setRootIssueId("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t(($) => $.playbook_tab.start_failed));
    }
  };

  return (
    <div className="space-y-5">
      <section className="space-y-2" aria-labelledby="playbook-definition-heading">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div>
            <h3 id="playbook-definition-heading" className="text-sm font-medium">
              {t(($) => $.playbook_tab.definition_title)}
            </h3>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t(($) => $.playbook_tab.definition_description)}
            </p>
          </div>
          {canManage && (
            <div className="flex items-center gap-2">
              {active && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  disabled={disabling}
                  onClick={() => void onDisable()}
                >
                  {disabling ? <Loader2 className="mr-1.5 size-3.5 animate-spin" /> : <Power className="mr-1.5 size-3.5" />}
                  {t(($) => $.playbook_tab.disable_button)}
                </Button>
              )}
              <Button
                type="button"
                size="sm"
                disabled={(active && !dirty) || saving}
                onClick={() => void save()}
              >
                {saving ? <Loader2 className="mr-1.5 size-3.5 animate-spin" /> : <Save className="mr-1.5 size-3.5" />}
                {active
                  ? t(($) => $.playbook_tab.save_button)
                  : t(($) => $.playbook_tab.enable_button)}
              </Button>
            </div>
          )}
        </div>

        <Textarea
          aria-label={t(($) => $.playbook_tab.definition_title)}
          value={draft}
          onChange={(event) => updateDraft(event.target.value)}
          readOnly={!canManage}
          spellCheck={false}
          className="min-h-72 resize-y font-mono text-xs leading-relaxed"
        />
        {dirty && <p className="text-xs text-muted-foreground">{t(($) => $.playbook_tab.unsaved_changes)}</p>}
      </section>

      {active && canManage && (
        <section className="space-y-2 border-t pt-5" aria-labelledby="playbook-start-heading">
          <div>
            <h3 id="playbook-start-heading" className="text-sm font-medium">
              {t(($) => $.playbook_tab.start_title)}
            </h3>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {t(($) => $.playbook_tab.start_description)}
            </p>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              aria-label={t(($) => $.playbook_tab.root_issue_label)}
              value={rootIssueId}
              onChange={(event) => setRootIssueId(event.target.value)}
              placeholder={t(($) => $.playbook_tab.root_issue_placeholder)}
              className="font-mono text-xs"
            />
            <Button
              type="button"
              variant="outline"
              disabled={!rootIssueId.trim() || starting}
              onClick={() => void start()}
            >
              {starting ? <Loader2 className="mr-1.5 size-3.5 animate-spin" /> : <Play className="mr-1.5 size-3.5" />}
              {t(($) => $.playbook_tab.start_button)}
            </Button>
          </div>
        </section>
      )}

      {error && (
        <p role="alert" className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
