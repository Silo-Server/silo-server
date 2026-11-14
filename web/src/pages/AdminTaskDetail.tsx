import { useState } from "react";
import { useParams, Link } from "react-router";
import { Play, Square, Plus, ChevronRight, ChevronDown } from "lucide-react";
import { isNotFoundProblem } from "@/api/v2/request";
import { Button } from "@/components/ui/button";
import PageUnavailable from "@/components/PageUnavailable";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { PageHeader } from "@/components/page/PageHeader";
import { Badge } from "@/components/ui/badge";
import { TaskStatusBadge } from "@/components/admin/TaskStatusBadge";
import {
  useTask,
  useTaskHistory,
  useRunTask,
  useCancelTask,
  useUpdateTriggers,
  useTaskMetrics,
  type MetadataRefreshMetrics,
  fetchTaskSchedule,
  taskMutationMessage,
  type TaskSchedule,
} from "@/hooks/queries/admin/tasks";
import type { ExecutionResult, TriggerConfig } from "@/api/types";
import { describeTrigger } from "@/lib/taskTrigger";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";
import { clampTaskProgress, formatTaskProgress } from "@/lib/taskProgress";
import { useServerText, useT, type MessageKey, type Translate } from "@/i18n";
import { taskText } from "@/lib/taskText";
import { TriggerFormRow } from "./AdminTaskTriggerForm";

const EM_DASH = "—";

const REFRESH_REASON_LABELS: Record<string, MessageKey> = {
  episode_incomplete: "web.admin.tasks.refreshReason.episodeIncomplete",
  stale_provider_id: "web.admin.tasks.refreshReason.staleProviderId",
  provider_id_incomplete: "web.admin.tasks.refreshReason.providerIdIncomplete",
  refresh_failure: "web.admin.tasks.refreshReason.refreshFailure",
  core_metadata_incomplete: "web.admin.tasks.refreshReason.coreMetadataIncomplete",
  trailers_requested: "web.admin.tasks.refreshReason.trailersRequested",
  provider_artwork: "web.admin.tasks.refreshReason.providerArtwork",
};

function refreshReasonLabel(t: Translate, reason: string): string {
  const key = REFRESH_REASON_LABELS[reason] as MessageKey | undefined;
  return key ? t(key) : reason;
}

function formatDuration(t: Translate, ms: number): string {
  if (ms < 1000) return t("web.admin.tasks.duration.milliseconds", { ms: ms });
  const totalSeconds = Math.floor(ms / 1000);
  if (totalSeconds < 60) {
    return t("web.admin.tasks.duration.seconds", { seconds: totalSeconds });
  }
  const minutes = Math.floor(totalSeconds / 60);
  const remainSec = totalSeconds - minutes * 60;
  return t("web.admin.tasks.duration.minutesSeconds", {
    minutes: minutes,
    seconds: remainSec,
  });
}

function formatDateTime(dateStr: string): string {
  return formatPreferredDateTime(dateStr);
}

function formatOptionalDateTime(dateStr?: string | null): string {
  return dateStr ? formatDateTime(dateStr) : EM_DASH;
}

function RefreshMetricsPanel({ metrics }: { metrics: MetadataRefreshMetrics }) {
  const t = useT();
  const reasonCounts = metrics.reason_counts.filter((entry) => entry.count > 0);

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            {t("web.admin.tasks.refreshMetrics.backlog")}
          </div>
          <div className="mt-2 text-2xl font-semibold">{metrics.total}</div>
        </div>
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            {t("web.admin.tasks.refreshMetrics.due")}
          </div>
          <div className="mt-2 text-2xl font-semibold">{metrics.due}</div>
        </div>
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            {t("web.admin.tasks.refreshMetrics.processing")}
          </div>
          <div className="mt-2 text-2xl font-semibold">{metrics.leased}</div>
        </div>
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            {t("web.admin.tasks.refreshMetrics.waitingSince")}
          </div>
          <div className="mt-2 text-sm font-medium">
            {formatOptionalDateTime(metrics.oldest_due_at)}
          </div>
        </div>
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            {t("web.admin.tasks.refreshMetrics.nextClaimTimeout")}
          </div>
          <div className="mt-2 text-sm font-medium">
            {formatOptionalDateTime(metrics.oldest_lease_expires_at)}
          </div>
        </div>
      </div>

      <div className="grid gap-4 xl:grid-cols-[1.2fr_0.8fr]">
        <div className="surface-panel rounded-2xl border-0 p-4">
          <h3 className="text-sm font-medium">{t("web.admin.tasks.detail.reasonBreakdown")}</h3>
          <div className="mt-3 flex flex-wrap gap-2">
            {reasonCounts.length === 0 && (
              <span className="text-muted-foreground text-sm">
                {t("web.admin.tasks.detail.noQueuedDebt")}
              </span>
            )}
            {reasonCounts.map((entry) => (
              <Badge key={entry.reason} variant="secondary">
                {t("web.admin.tasks.refreshMetrics.reasonCount", {
                  reason: refreshReasonLabel(t, entry.reason),
                  count: entry.count,
                })}
              </Badge>
            ))}
          </div>
          <div className="mt-4 grid gap-2 sm:grid-cols-5">
            {metrics.attempt_buckets.map((bucket) => (
              <div key={bucket.label} className="bg-muted/30 rounded-xl p-3">
                <div className="text-muted-foreground text-xs">
                  {t("web.admin.tasks.detail.attemptBucket", { range: bucket.label })}
                </div>
                <div className="mt-1 text-lg font-semibold">{bucket.count}</div>
              </div>
            ))}
          </div>
        </div>

        <div className="surface-panel rounded-2xl border-0 p-4">
          <h3 className="text-sm font-medium">{t("web.admin.tasks.detail.recentErrors")}</h3>
          <div className="mt-3 space-y-3">
            {metrics.recent_errors.length === 0 && (
              <p className="text-muted-foreground text-sm">
                {t("web.admin.tasks.detail.noRecentErrors")}
              </p>
            )}
            {metrics.recent_errors.map((entry) => (
              <div
                key={`${entry.content_id}-${entry.last_error}`}
                className="bg-muted/20 rounded-xl p-3"
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">
                      {entry.title || entry.content_id}
                    </div>
                    <div className="text-muted-foreground text-xs">
                      {t("web.admin.tasks.detail.recentErrorAttempts", {
                        type: entry.type || t("web.admin.tasks.detail.itemFallback"),
                        count: entry.attempt_count,
                        count_text: entry.attempt_count,
                      })}
                    </div>
                  </div>
                  <div className="text-muted-foreground shrink-0 text-xs">
                    {formatOptionalDateTime(entry.last_attempt_at)}
                  </div>
                </div>
                <div className="text-muted-foreground mt-2 line-clamp-2 text-xs">
                  {entry.last_error}
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>

      <div className="surface-panel overflow-hidden rounded-2xl border-0">
        <div className="border-border border-b px-4 py-3">
          <h3 className="text-sm font-medium">{t("web.admin.tasks.detail.dueSamples")}</h3>
        </div>
        <table className="w-full text-sm">
          <thead>
            <tr className="border-border bg-muted/40 border-b">
              <th className="px-4 py-2 text-start font-medium">
                {t("web.admin.tasks.detail.columns.item")}
              </th>
              <th className="px-4 py-2 text-start font-medium">
                {t("web.admin.tasks.detail.columns.nextRefresh")}
              </th>
              <th className="px-4 py-2 text-start font-medium">
                {t("web.admin.tasks.detail.columns.attempts")}
              </th>
              <th className="px-4 py-2 text-start font-medium">
                {t("web.admin.tasks.detail.columns.lastAttempt")}
              </th>
            </tr>
          </thead>
          <tbody>
            {metrics.due_samples.length === 0 && (
              <tr>
                <td colSpan={4} className="text-muted-foreground px-4 py-4 text-center">
                  {t("web.admin.tasks.detail.noDueItems")}
                </td>
              </tr>
            )}
            {metrics.due_samples.map((entry) => (
              <tr key={entry.content_id} className="border-border border-b last:border-b-0">
                <td className="px-4 py-2">
                  <div className="font-medium">{entry.title || entry.content_id}</div>
                  <div className="text-muted-foreground text-xs">
                    {entry.type || t("web.admin.tasks.detail.itemFallback")}
                  </div>
                </td>
                <td className="px-4 py-2">{formatDateTime(entry.next_refresh_at)}</td>
                <td className="px-4 py-2">{entry.attempt_count}</td>
                <td className="px-4 py-2">{formatOptionalDateTime(entry.last_attempt_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function TaskSchedulePanel({
  taskKey,
  manualOnly,
  triggers,
}: {
  taskKey: string;
  manualOnly: boolean;
  triggers: TriggerConfig[];
}) {
  const t = useT();
  const update = useUpdateTriggers();
  const [snapshot, setSnapshot] = useState<TaskSchedule | null>(null);
  const [draft, setDraft] = useState<TriggerConfig[]>([]);
  const [candidate, setCandidate] = useState<TaskSchedule | null>(null);
  const [busy, setBusy] = useState(false);
  const [needsReview, setNeedsReview] = useState(false);
  const [error, setError] = useState("");
  const startEditing = async () => {
    setBusy(true);
    setError("");
    try {
      const value = await fetchTaskSchedule(taskKey);
      setSnapshot(value);
      setDraft(value.triggers);
      setNeedsReview(false);
    } catch (error) {
      setError(taskMutationMessage(error));
    } finally {
      setBusy(false);
    }
  };
  const review = async () => {
    setBusy(true);
    setCandidate(null);
    setError("");
    try {
      setCandidate(await fetchTaskSchedule(taskKey));
    } catch (error) {
      setError(taskMutationMessage(error));
    } finally {
      setBusy(false);
    }
  };
  const save = async () => {
    if (!snapshot) return;
    setError("");
    try {
      await update.mutateAsync({ key: taskKey, triggers: draft, etag: snapshot.etag });
      setSnapshot(null);
      setCandidate(null);
    } catch (error) {
      setNeedsReview(true);
      setError(taskMutationMessage(error));
    }
  };
  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-lg font-medium">{t("web.admin.tasks.detail.schedule")}</h2>
        {!manualOnly && !snapshot && (
          <Button variant="outline" size="sm" disabled={busy} onClick={() => void startEditing()}>
            {t("web.admin.tasks.detail.editSchedule")}
          </Button>
        )}
      </div>
      {error && (
        <p role="alert" className="text-destructive text-sm">
          {error}
        </p>
      )}
      {manualOnly ? (
        <p className="text-muted-foreground text-sm">{t("web.admin.tasks.detail.manualOnly")}</p>
      ) : !snapshot ? (
        <div className="surface-panel rounded-2xl p-4">
          {triggers.length === 0
            ? t("web.admin.tasks.detail.noTriggers")
            : triggers.map((trigger, i) => (
                <p key={i} className="py-2 text-sm">
                  {describeTrigger(trigger)}
                </p>
              ))}
        </div>
      ) : (
        <div className="surface-panel space-y-3 rounded-2xl p-4">
          <fieldset disabled={update.isPending || busy} className="space-y-3">
            {draft.map((trigger, i) => (
              <TriggerFormRow
                key={i}
                trigger={trigger}
                onChange={(value) =>
                  setDraft(draft.map((old, index) => (index === i ? value : old)))
                }
                onRemove={() => setDraft(draft.filter((_, index) => index !== i))}
              />
            ))}
            <Button
              variant="outline"
              size="sm"
              onClick={() => setDraft([...draft, { type: "interval", interval_ms: 3_600_000 }])}
            >
              <Plus className="me-1.5 h-3.5 w-3.5" />
              {t("web.admin.tasks.detail.addTrigger")}
            </Button>
          </fieldset>
          {needsReview && (
            <div className="space-y-2 text-sm">
              <Button
                variant="outline"
                disabled={busy || update.isPending}
                onClick={() => void review()}
              >
                {t("web.admin.tasks.detail.reviewSchedule")}
              </Button>
              {candidate && (
                <div className="space-y-2">
                  <p>{t("web.admin.tasks.detail.currentSavedSchedule")}</p>
                  {candidate.triggers.length === 0 ? (
                    <p>{t("web.admin.tasks.detail.noTriggers")}</p>
                  ) : (
                    candidate.triggers.map((trigger, i) => (
                      <p key={i}>{describeTrigger(trigger)}</p>
                    ))
                  )}
                  <Button
                    disabled={busy || update.isPending}
                    onClick={() => {
                      setSnapshot(candidate);
                      setCandidate(null);
                      setNeedsReview(false);
                      setError("");
                    }}
                  >
                    {t("web.admin.tasks.detail.useRevision")}
                  </Button>
                </div>
              )}
            </div>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              size="sm"
              onClick={() => void save()}
              disabled={needsReview || busy || update.isPending}
            >
              {t("common.save")}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              disabled={busy || update.isPending}
              onClick={() => {
                setSnapshot(null);
                setCandidate(null);
                setError("");
              }}
            >
              {t("common.cancel")}
            </Button>
          </div>
        </div>
      )}
    </section>
  );
}

// --- History row with expandable result_data ---

function HistoryRow({
  result,
  expanded,
  onToggle,
}: {
  result: ExecutionResult;
  expanded: boolean;
  onToggle: () => void;
}) {
  const t = useT();
  const hasResultData = result.result_data && Object.keys(result.result_data).length > 0;
  const failedSteps = (result.steps ?? [])
    .filter((step) => step.status === "failed")
    .map((step) => step.name);
  const errorText =
    failedSteps.length > 0
      ? t("web.admin.tasks.detail.history.failedSteps", { steps: failedSteps.join(", ") })
      : result.error_message || EM_DASH;

  return (
    <>
      <tr
        className={`border-border border-b last:border-b-0 ${
          result.status === "failed" ? "bg-destructive/5" : ""
        } ${hasResultData ? "hover:bg-muted/30 cursor-pointer" : ""}`}
        onClick={hasResultData ? onToggle : undefined}
      >
        <td className="px-4 py-2">
          <span className="inline-flex items-center gap-1.5">
            {hasResultData &&
              (expanded ? (
                <ChevronDown className="h-3.5 w-3.5 shrink-0" />
              ) : (
                <ChevronRight className="h-3.5 w-3.5 shrink-0" />
              ))}
            {formatDateTime(result.started_at)}
          </span>
        </td>
        <td className="px-4 py-2">{formatDuration(t, result.duration_ms)}</td>
        <td className="px-4 py-2">
          <TaskStatusBadge result={result} />
        </td>
        <td className="text-muted-foreground max-w-xs truncate px-4 py-2" title={errorText}>
          {errorText}
        </td>
      </tr>
      {expanded && hasResultData && (
        <tr className="border-border border-b last:border-b-0">
          <td colSpan={4} className="bg-muted/20 px-4 py-3">
            <pre className="text-muted-foreground overflow-x-auto text-xs">
              {JSON.stringify(result.result_data, null, 2)}
            </pre>
          </td>
        </tr>
      )}
    </>
  );
}

// --- Main page ---

export default function AdminTaskDetail() {
  const t = useT();
  const serverText = useServerText();
  const { key } = useParams<{ key: string }>();
  const { data: cachedTask, isLoading, isFetching, error, refetch } = useTask(key!);
  // A 404 outranks a cached task: a refetch that finds it gone must not leave
  // its old runtime state on screen.
  const task = isNotFoundProblem(error) ? undefined : cachedTask;
  const historyQuery = useTaskHistory(key!);
  const history = historyQuery.data;
  const { data: metrics } = useTaskMetrics(key!);
  const runTask = useRunTask();
  const cancelTask = useCancelTask();

  const [expandedRows, setExpandedRows] = useState<Set<string>>(new Set());

  const toggleRow = (id: string) => {
    setExpandedRows((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  if (isLoading) {
    return (
      <p className="text-muted-foreground pb-8 text-sm">{t("web.admin.tasks.detail.loading")}</p>
    );
  }

  // A key that names no task used to leave this on "Loading..." for good.
  if (!task) {
    if (error && !isNotFoundProblem(error)) {
      return (
        <PageUnavailable
          title={t("web.admin.tasks.detail.loadErrorTitle")}
          description={t("web.admin.tasks.detail.loadErrorDescription")}
          onRetry={() => void refetch()}
          retrying={isFetching}
        />
      );
    }
    return (
      <PageUnavailable
        title={t("web.admin.tasks.detail.notFoundTitle")}
        description={t("web.admin.tasks.detail.notFoundDescription")}
      >
        <Button asChild variant="outline">
          <ViewTransitionLink to="/admin/tasks" up>
            {t("web.admin.tasks.detail.allTasks")}
          </ViewTransitionLink>
        </Button>
      </PageUnavailable>
    );
  }

  const isRunning = task.state === "running" || task.state === "cancelling";

  return (
    <div className="space-y-6 pb-4 sm:pb-6">
      <PageHeader
        eyebrow={
          <nav
            aria-label={t("web.admin.tasks.detail.breadcrumb.label")}
            className="text-muted-foreground flex items-center gap-1.5 text-sm"
          >
            <Link to="/admin" className="hover:text-foreground transition-colors">
              {t("web.admin.tasks.detail.breadcrumb.admin")}
            </Link>
            <ChevronRight className="h-3.5 w-3.5" />
            <Link to="/admin/tasks" className="hover:text-foreground transition-colors">
              {t("web.admin.tasks.detail.breadcrumb.tasks")}
            </Link>
            <ChevronRight className="h-3.5 w-3.5" />
            <span className="text-foreground font-medium">
              {taskText(serverText, task, "name")}
            </span>
          </nav>
        }
        title={taskText(serverText, task, "name")}
        subtitle={taskText(serverText, task, "description")}
        actions={
          <>
            <Badge variant="outline">{task.category}</Badge>
            {isRunning ? (
              <Button
                variant="outline"
                className="w-full sm:w-auto"
                onClick={() => cancelTask.mutate(task.key)}
                disabled={cancelTask.isPending || task.state === "cancelling"}
              >
                <Square className="me-1.5 h-4 w-4" />
                {t("web.admin.tasks.detail.cancelRun")}
              </Button>
            ) : (
              <Button
                variant="outline"
                className="w-full sm:w-auto"
                onClick={() => runTask.mutate(task.key)}
                disabled={runTask.isPending}
              >
                <Play className="me-1.5 h-4 w-4" />
                {t("web.admin.tasks.actions.runNow")}
              </Button>
            )}
          </>
        }
      />

      {task.key === "scan_libraries" && (
        <div className="surface-panel-subtle text-muted-foreground rounded-xl p-4 text-sm leading-relaxed">
          {t("web.admin.tasks.detail.scanLibrariesNote", {
            link: (chunks) => (
              <Link
                to="/admin/libraries"
                className="text-foreground hover:text-primary font-medium"
              >
                {chunks}
              </Link>
            ),
          })}
        </div>
      )}

      {isRunning && (
        <div className="surface-panel-subtle space-y-1 rounded-xl p-4">
          <div className="bg-muted h-2.5 w-full overflow-hidden rounded-full">
            <div
              className={`h-full rounded-full transition-all duration-300 ${
                task.state === "cancelling" ? "bg-yellow-500" : "bg-primary"
              }`}
              style={{ width: `${Math.max(clampTaskProgress(task.progress), 2)}%` }}
            />
          </div>
          <div className="text-muted-foreground flex items-center justify-between gap-3 text-sm">
            <p className="min-w-0 truncate">
              {task.state === "cancelling"
                ? t("web.admin.tasks.progress.cancelling")
                : t("web.admin.tasks.progress.running")}
            </p>
            {task.state !== "cancelling" && task.progress > 0 && (
              <span className="shrink-0 font-medium tabular-nums">
                {formatTaskProgress(task.progress)}
              </span>
            )}
          </div>
        </div>
      )}

      {task.key === "refresh_metadata" && metrics && (
        <div className="space-y-3">
          <h2 className="text-lg font-medium tracking-tight">
            {t("web.admin.tasks.detail.queueHealth")}
          </h2>
          <RefreshMetricsPanel metrics={metrics} />
        </div>
      )}

      <TaskSchedulePanel
        key={task.key}
        taskKey={task.key}
        manualOnly={task.manual_only}
        triggers={task.triggers}
      />

      <div className="space-y-3">
        <h2 className="text-lg font-medium tracking-tight">
          {t("web.admin.tasks.detail.executionHistory")}
        </h2>

        {historyQuery.isError && (
          <div role="alert">
            {t("web.admin.tasks.detail.history.loadError", {
              restart: (chunks) => (
                <Button variant="outline" onClick={() => void historyQuery.restart()}>
                  {chunks}
                </Button>
              ),
            })}
          </div>
        )}
        <div className="surface-panel overflow-hidden rounded-2xl border-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-border bg-muted/50 border-b">
                <th className="px-4 py-2 text-start font-medium">
                  {t("web.admin.tasks.detail.history.columns.started")}
                </th>
                <th className="px-4 py-2 text-start font-medium">
                  {t("web.admin.tasks.detail.history.columns.duration")}
                </th>
                <th className="px-4 py-2 text-start font-medium">
                  {t("web.admin.tasks.detail.history.columns.status")}
                </th>
                <th className="px-4 py-2 text-start font-medium">
                  {t("web.admin.tasks.detail.history.columns.error")}
                </th>
              </tr>
            </thead>
            <tbody>
              {(!history || history.length === 0) && (
                <tr>
                  <td colSpan={4} className="text-muted-foreground px-4 py-4 text-center">
                    {t("web.admin.tasks.detail.history.empty")}
                  </td>
                </tr>
              )}
              {history?.map((result) => (
                <HistoryRow
                  key={result.id}
                  result={result}
                  expanded={expandedRows.has(result.id)}
                  onToggle={() => toggleRow(result.id)}
                />
              ))}
            </tbody>
          </table>
        </div>
        {historyQuery.hasNextPage && (
          <Button
            variant="outline"
            disabled={historyQuery.isFetchingNextPage}
            onClick={() => void historyQuery.fetchNextPage()}
          >
            {t("web.admin.tasks.detail.history.loadOlder")}
          </Button>
        )}
      </div>
    </div>
  );
}
