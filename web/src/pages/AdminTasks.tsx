import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { AlertTriangle, Loader2, Play, Square } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { TaskStatusBadge } from "@/components/admin/TaskStatusBadge";
import {
  useTasks,
  useRunTask,
  useCancelTask,
  useTaskMetrics,
  type MetadataRefreshMetrics,
} from "@/hooks/queries/admin/tasks";
import { usePageActivity } from "@/hooks/usePageActivity";
import { cn } from "@/lib/utils";
import type { TaskCategory, TaskInfo, TriggerConfig } from "@/api/types";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";

const CATEGORY_ORDER: TaskCategory[] = ["library", "metadata", "system"];
const RUN_BUTTON_MIN_VISIBLE_MS = 1_000;

const CATEGORY_LABELS: Record<TaskCategory, string> = {
  library: "Library",
  metadata: "Metadata",
  system: "System",
};

const REFRESH_REASON_LABELS: Record<string, string> = {
  episode_incomplete: "Episode incomplete",
  stale_provider_id: "Stale provider ID",
  refresh_failure: "Refresh failure",
  core_metadata_incomplete: "Core metadata incomplete",
};

function useTaskClock() {
  const pageActivity = usePageActivity();
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!pageActivity.canApplyRealtimeUpdates) {
      return;
    }

    setNow(Date.now());
    const id = window.setInterval(() => setNow(Date.now()), 30_000);
    return () => window.clearInterval(id);
  }, [pageActivity.canApplyRealtimeUpdates]);

  return now;
}

function formatRelativeTime(dateStr: string, now: number): string {
  const diff = Math.max(0, now - new Date(dateStr).getTime());
  const seconds = Math.floor(diff / 1000);
  if (seconds < 60) return "just now";
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

function formatDuration(ms: number): string {
  if (ms <= 0) return "<1ms";
  if (ms < 1000) return `${ms}ms`;
  const totalSeconds = Math.floor(ms / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const remainSec = totalSeconds - minutes * 60;
  if (minutes < 60) return `${minutes}m ${remainSec}s`;
  const hours = Math.floor(minutes / 60);
  const remainMinutes = minutes % 60;
  return `${hours}h ${remainMinutes}m`;
}

function numberFromResultData(data: Record<string, unknown> | undefined, key: string) {
  const value = data?.[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function formatTaskResultSummary(task: TaskInfo): string | null {
  const resultData = task.last_execution?.result_data;
  if (!resultData || task.key !== "refresh_trending_discover") {
    return null;
  }

  const combos = numberFromResultData(resultData, "combos");
  const refreshed = numberFromResultData(resultData, "refreshed");
  const empty = numberFromResultData(resultData, "empty");
  const failed = numberFromResultData(resultData, "failed");
  if (combos == null || refreshed == null || empty == null || failed == null) {
    return null;
  }

  if (combos === 0) {
    return "No enabled Trending Discover sections";
  }

  return `${refreshed} refreshed, ${empty} empty, ${failed} failed`;
}

function describeTrigger(t: TriggerConfig): string {
  switch (t.type) {
    case "interval": {
      const ms = t.interval_ms ?? 0;
      if (ms >= 86_400_000) return `Every ${Math.round(ms / 86_400_000)}d`;
      if (ms >= 3_600_000) return `Every ${Math.round(ms / 3_600_000)}h`;
      if (ms >= 60_000) return `Every ${Math.round(ms / 60_000)}m`;
      return `Every ${Math.round(ms / 1000)}s`;
    }
    case "daily":
      return `Daily at ${t.time_of_day ?? "00:00"}`;
    case "weekly": {
      const days = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
      return `${days[t.day_of_week ?? 0]} at ${t.time_of_day ?? "00:00"}`;
    }
    case "startup":
      return "On startup";
    default:
      return t.type;
  }
}

function describeSchedule(triggers: TriggerConfig[]): string | null {
  if (triggers.length === 0) return null;
  return triggers.map(describeTrigger).join(", ");
}

function isOverdue(dateStr: string, now: number): boolean {
  return new Date(dateStr).getTime() < now;
}

function formatNextRun(dateStr: string, now: number): string {
  const diff = new Date(dateStr).getTime() - now;
  if (diff < 0) return "overdue";
  const minutes = Math.floor(diff / 60_000);
  if (minutes < 60) return `in ${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `in ${hours}h`;
  const days = Math.floor(hours / 24);
  return `in ${days}d`;
}

function formatDateTime(dateStr?: string | null): string {
  if (!dateStr) return "—";
  return formatPreferredDateTime(dateStr);
}

function delay(ms: number) {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
}

function RefreshMetadataSummary({ metrics }: { metrics: MetadataRefreshMetrics }) {
  const topReasons = metrics.reason_counts.filter((entry) => entry.count > 0);

  return (
    <div className="bg-muted/20 mt-3 rounded-xl p-3">
      <div className="grid gap-2 text-xs sm:grid-cols-5">
        <div>
          <div className="text-muted-foreground">Refresh Backlog</div>
          <div className="text-foreground font-medium">{metrics.total}</div>
        </div>
        <div>
          <div className="text-muted-foreground">Due for Refresh</div>
          <div className="text-foreground font-medium">{metrics.due}</div>
        </div>
        <div>
          <div className="text-muted-foreground">Processing</div>
          <div className="text-foreground font-medium">{metrics.leased}</div>
        </div>
        <div>
          <div className="text-muted-foreground">Waiting Since</div>
          <div className="text-foreground font-medium">{formatDateTime(metrics.oldest_due_at)}</div>
        </div>
        <div>
          <div className="text-muted-foreground">Next Claim Timeout</div>
          <div className="text-foreground font-medium">
            {formatDateTime(metrics.oldest_lease_expires_at)}
          </div>
        </div>
      </div>

      {topReasons.length > 0 && (
        <div className="mt-3 flex flex-wrap gap-2">
          {topReasons.map((entry) => (
            <Badge key={entry.reason} variant="secondary">
              {REFRESH_REASON_LABELS[entry.reason] ?? entry.reason}: {entry.count}
            </Badge>
          ))}
        </div>
      )}
    </div>
  );
}

function TaskRow({
  task,
  refreshMetrics,
  now,
}: {
  task: TaskInfo;
  refreshMetrics?: MetadataRefreshMetrics;
  now: number;
}) {
  const runTask = useRunTask();
  const cancelTask = useCancelTask();
  const runFeedbackStartedAtRef = useRef<number | null>(null);
  const [isRunFeedbackVisible, setIsRunFeedbackVisible] = useState(false);

  const isRunning = task.state === "running" || task.state === "cancelling";
  const isShowingRunFeedback = isRunning || isRunFeedbackVisible;
  const resultSummary = formatTaskResultSummary(task);

  const handleRunTask = async () => {
    runFeedbackStartedAtRef.current = Date.now();
    setIsRunFeedbackVisible(true);

    try {
      await runTask.mutateAsync(task.key);
    } finally {
      const startedAt = runFeedbackStartedAtRef.current;
      if (startedAt !== null) {
        const remaining = RUN_BUTTON_MIN_VISIBLE_MS - (Date.now() - startedAt);
        if (remaining > 0) {
          await delay(remaining);
        }
      }
      runFeedbackStartedAtRef.current = null;
      setIsRunFeedbackVisible(false);
    }
  };

  return (
    <div className="border-border flex flex-col gap-4 border-b px-4 py-4 last:border-b-0 sm:flex-row sm:items-center">
      <div className="min-w-0 flex-1">
        <Link
          to={`/admin/tasks/${task.key}`}
          className="text-foreground hover:text-primary text-sm font-medium transition-colors"
        >
          {task.name}
        </Link>

        <div className="text-muted-foreground mt-0.5 text-xs">
          {task.state === "idle" && (
            <>
              {describeSchedule(task.triggers) && <span>{describeSchedule(task.triggers)}</span>}
              {!describeSchedule(task.triggers) && <span>No schedule</span>}
              {task.last_execution && (
                <span className="ml-2">
                  · Last run: {formatRelativeTime(task.last_execution.completed_at, now)}
                  {typeof task.last_execution.duration_ms === "number"
                    ? ` · Duration: ${formatDuration(task.last_execution.duration_ms)}`
                    : ""}
                  {resultSummary ? ` · Result: ${resultSummary}` : ""}
                </span>
              )}
              {!task.last_execution && !describeSchedule(task.triggers) && (
                <span> · Never run</span>
              )}
              {task.next_run_at &&
                (isOverdue(task.next_run_at, now) ? (
                  <span className="text-warning ml-2 inline-flex items-center gap-1 font-medium">
                    <AlertTriangle className="h-3 w-3" aria-hidden="true" />
                    Overdue
                  </span>
                ) : (
                  <span className="ml-2">· Next: {formatNextRun(task.next_run_at, now)}</span>
                ))}
            </>
          )}
        </div>

        {isRunning && (
          <div className="mt-1.5 space-y-1">
            <div className="bg-muted h-2 w-full overflow-hidden rounded-full">
              <div
                className={`h-full rounded-full transition-all duration-300 ${
                  task.state === "cancelling" ? "bg-yellow-500" : "bg-primary"
                }`}
                style={{ width: `${Math.max(task.progress, 2)}%` }}
              />
            </div>
            <p className="text-muted-foreground text-xs">
              {task.state === "cancelling"
                ? "Cancelling..."
                : task.progress_message || `${Math.round(task.progress)}%`}
            </p>
          </div>
        )}

        {task.key === "refresh_metadata" && refreshMetrics && (
          <RefreshMetadataSummary metrics={refreshMetrics} />
        )}
      </div>

      {task.state === "idle" && task.last_execution && (
        <TaskStatusBadge result={task.last_execution} className="self-start sm:self-center" />
      )}

      {isShowingRunFeedback ? (
        <Button
          variant="outline"
          size="sm"
          className={cn(
            "w-full min-w-[8.25rem] justify-center sm:w-auto",
            isRunning &&
              task.state !== "cancelling" &&
              "text-destructive hover:border-destructive/60 hover:bg-destructive/10 hover:text-destructive",
          )}
          onClick={() => cancelTask.mutate(task.key)}
          disabled={!isRunning || cancelTask.isPending || task.state === "cancelling"}
          title={isRunning ? "Stop this task run" : undefined}
          aria-busy={isShowingRunFeedback}
        >
          {task.state === "cancelling" ? (
            <Square className="mr-1.5 h-3.5 w-3.5 fill-current" />
          ) : isRunning ? (
            <Square className="mr-1.5 h-3.5 w-3.5 fill-current" />
          ) : (
            <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
          )}
          {task.state === "cancelling" ? "Stopping..." : isRunning ? "Stop" : "Starting..."}
        </Button>
      ) : (
        <Button
          variant="outline"
          size="sm"
          className="w-full min-w-[8.25rem] justify-center sm:w-auto"
          onClick={() => {
            void handleRunTask();
          }}
          disabled={runTask.isPending}
        >
          <Play className="mr-1.5 h-3.5 w-3.5" />
          Run Now
        </Button>
      )}
    </div>
  );
}

export default function AdminTasks() {
  useEventChannel("tasks");
  const now = useTaskClock();
  const { data: tasks, isLoading } = useTasks();
  const { data: refreshMetrics } = useTaskMetrics("refresh_metadata");

  const grouped = CATEGORY_ORDER.map((cat) => ({
    category: cat,
    label: CATEGORY_LABELS[cat],
    tasks: (tasks ?? []).filter((t) => t.category === cat),
  })).filter((g) => g.tasks.length > 0);

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <div className="page-header gap-5">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Scheduled Tasks</h1>
          <p className="page-subtitle text-sm sm:text-base">
            View and manage background tasks. You can trigger tasks manually or adjust their
            schedules, including whether a task runs on server startup.
          </p>
        </div>
      </div>

      {isLoading && <p className="text-muted-foreground text-sm">Loading tasks...</p>}

      {grouped.map((group) => (
        <div key={group.category} className="space-y-3">
          <h2 className="text-muted-foreground text-xs font-medium tracking-[0.24em] uppercase">
            {group.label}
          </h2>
          <div className="surface-panel overflow-hidden rounded-2xl border-0">
            {group.tasks.map((task) => (
              <TaskRow
                key={task.key}
                task={task}
                now={now}
                refreshMetrics={task.key === "refresh_metadata" ? refreshMetrics : undefined}
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
