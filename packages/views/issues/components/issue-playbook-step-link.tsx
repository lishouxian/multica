"use client";

import type { Issue, PlaybookNodeRun, PlaybookRun } from "@multica/core/types";
import { useWorkspacePaths } from "@multica/core/paths";
import { Badge } from "@multica/ui/components/ui/badge";
import { Workflow } from "lucide-react";
import { AppLink } from "../../navigation";
import { useT } from "../../i18n";
import { StatusIcon } from "./status-icon";

interface IssuePlaybookStepLinkProps {
  parentIssue: Issue;
  run: PlaybookRun;
  node: PlaybookNodeRun;
}

export function IssuePlaybookStepLink({
  parentIssue,
  run,
  node,
}: IssuePlaybookStepLinkProps) {
  const { t } = useT("issues");
  const paths = useWorkspacePaths();

  return (
    <AppLink
      href={paths.issueDetail(parentIssue.id)}
      className="mt-2 inline-flex max-w-full items-center gap-1.5 rounded-md bg-primary/5 px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-primary/10 hover:text-foreground"
    >
      <Workflow className="size-3.5 shrink-0 text-primary" aria-hidden="true" />
      <span className="shrink-0 font-medium text-foreground">
        {t(($) => $.detail.playbook_step)}
      </span>
      <span className="shrink-0 font-mono">{node.step_key}</span>
      <span aria-hidden="true">·</span>
      <StatusIcon status={parentIssue.status} className="size-3.5 shrink-0" />
      <span className="shrink-0 tabular-nums">{parentIssue.identifier}</span>
      <span className="truncate">{parentIssue.title}</span>
      <Badge variant="outline" className="ml-1 shrink-0 font-mono text-[10px]">
        #{run.id.slice(0, 8)}
      </Badge>
    </AppLink>
  );
}
