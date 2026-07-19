"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, Play, Workflow } from "lucide-react";
import {
  workflowListOptions,
  workflowRunsOptions,
} from "@multica/core/workflows/queries";
import type { WorkflowDefinition } from "@multica/core/workflows";
import { useWorkspaceId } from "@multica/core/hooks";
import { useWorkspacePaths } from "@multica/core/paths";
import { Button } from "@multica/ui/components/ui/button";
import {
  Tabs,
  TabsList,
  TabsTrigger,
  TabsContent,
} from "@multica/ui/components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@multica/ui/components/ui/table";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import {
  CollectionPageHeader,
  CollectionPageState,
} from "../../layout/collection-page";
import { useNavigation } from "../../navigation";
import { useT } from "../../i18n";
import { WorkflowStatusBadge } from "./workflow-status-badge";
import { RunWorkflowDialog } from "./run-workflow-dialog";
import { WorkflowDetailDialog } from "./workflow-detail-dialog";

export function WorkflowsPage() {
  const { t } = useT("workflows");
  const wsId = useWorkspaceId();
  const paths = useWorkspacePaths();
  const nav = useNavigation();

  const definitions = useQuery(workflowListOptions(wsId));
  const runs = useQuery(workflowRunsOptions(wsId));

  const [runTarget, setRunTarget] = useState<WorkflowDefinition | null>(null);
  const [detailTarget, setDetailTarget] = useState<WorkflowDefinition | null>(null);

  const activeRunsCount = useMemo(
    () => (runs.data ?? []).filter((r) => r.status === "running").length,
    [runs.data],
  );

  return (
    <div className="relative flex flex-1 min-h-0 flex-col">
      <CollectionPageHeader
        icon={Workflow}
        title={t(($) => $.title)}
        description={t(($) => $.subtitle)}
      />

      <Tabs defaultValue="definitions" className="flex flex-1 min-h-0 flex-col">
        <div className="px-5 pt-3">
          <TabsList>
            <TabsTrigger value="definitions">
              {t(($) => $.tabs.definitions)}
            </TabsTrigger>
            <TabsTrigger value="runs">
              {t(($) => $.tabs.runs)}
              {activeRunsCount > 0 ? (
                <span className="ml-1.5 font-mono text-xs tabular-nums text-muted-foreground">
                  {activeRunsCount}
                </span>
              ) : null}
            </TabsTrigger>
          </TabsList>
        </div>

        {/* Playbooks */}
        <TabsContent value="definitions" className="flex-1 overflow-y-auto px-5 pb-8">
          {definitions.isError ? (
            <CollectionPageState
              role="alert"
              tone="destructive"
              icon={AlertCircle}
              title={
                definitions.error instanceof Error
                  ? definitions.error.message
                  : String(definitions.error)
              }
            />
          ) : definitions.isLoading ? (
            <TableSkeleton />
          ) : (definitions.data ?? []).length === 0 ? (
            <CollectionPageState
              icon={Workflow}
              title={t(($) => $.definitions.empty)}
            />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t(($) => $.tabs.definitions)}</TableHead>
                  <TableHead className="w-32">{t(($) => $.definitions.steps, { count: 0 })}</TableHead>
                  <TableHead className="w-24" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {(definitions.data ?? []).map((wf) => (
                  <TableRow
                    key={wf.id}
                    className="cursor-pointer"
                    onClick={() => setDetailTarget(wf)}
                  >
                    <TableCell className="font-medium">{wf.name}</TableCell>
                    <TableCell className="text-muted-foreground">
                      {t(($) => $.definitions.steps, { count: wf.steps.length })}
                    </TableCell>
                    <TableCell className="text-right">
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={(e) => {
                          // Row click opens the detail dialog; keep Run its
                          // own action.
                          e.stopPropagation();
                          setRunTarget(wf);
                        }}
                      >
                        <Play className="size-3.5" />
                        {t(($) => $.definitions.run)}
                      </Button>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </TabsContent>

        {/* Runs */}
        <TabsContent value="runs" className="flex-1 overflow-y-auto px-5 pb-8">
          {runs.isError ? (
            <CollectionPageState
              role="alert"
              tone="destructive"
              icon={AlertCircle}
              title={
                runs.error instanceof Error ? runs.error.message : String(runs.error)
              }
            />
          ) : runs.isLoading ? (
            <TableSkeleton />
          ) : (runs.data ?? []).length === 0 ? (
            <CollectionPageState icon={Workflow} title={t(($) => $.runs.empty)} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t(($) => $.runs.col_run)}</TableHead>
                  <TableHead>{t(($) => $.runs.col_workflow)}</TableHead>
                  <TableHead className="w-40">{t(($) => $.runs.col_status)}</TableHead>
                  <TableHead>{t(($) => $.runs.col_stage)}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {(runs.data ?? []).map((run) => (
                  <TableRow
                    key={run.root_issue_id}
                    className="cursor-pointer"
                    onClick={() => nav.push(paths.issueDetail(run.root_issue_id))}
                  >
                    <TableCell className="font-mono text-xs">
                      {run.root_identifier || run.root_title}
                    </TableCell>
                    <TableCell>{run.workflow}</TableCell>
                    <TableCell>
                      <WorkflowStatusBadge status={run.status} />
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {run.frontier.join(", ") || "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </TabsContent>
      </Tabs>

      <RunWorkflowDialog
        workflow={runTarget}
        open={runTarget !== null}
        onOpenChange={(open) => {
          if (!open) setRunTarget(null);
        }}
        onStarted={(rootIssueId) => nav.push(paths.issueDetail(rootIssueId))}
      />
      <WorkflowDetailDialog
        workflow={detailTarget}
        open={detailTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDetailTarget(null);
        }}
        onRun={(wf) => setRunTarget(wf)}
      />
    </div>
  );
}

function TableSkeleton() {
  return (
    <div className="flex flex-col gap-2 pt-3">
      {Array.from({ length: 4 }).map((_, i) => (
        <Skeleton key={i} className="h-10 w-full" />
      ))}
    </div>
  );
}
