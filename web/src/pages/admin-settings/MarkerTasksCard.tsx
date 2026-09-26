import { useState } from "react";
import { Link } from "react-router";
import { Loader2, Play } from "lucide-react";

import type { TaskInfo } from "@/api/types";
import { TaskStatusBadge } from "@/components/admin/TaskStatusBadge";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { Button } from "@/components/ui/button";
import { useTasks, useRunTask } from "@/hooks/queries/admin/tasks";
import { formatDateTime } from "@/lib/datetime";

function numberFromResultData(data: Record<string, unknown> | undefined, key: string) {
  const value = data?.[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function formatTaskResult(task: TaskInfo | undefined) {
  const result = task?.last_execution;
  if (!result) return "Last run: Never run";

  const data = result.result_data;
  if (task.key === "contribute_markers") {
    const submitted = numberFromResultData(data, "submitted");
    const skipped = numberFromResultData(data, "skipped");
    const invalid = numberFromResultData(data, "invalid");
    const failed = numberFromResultData(data, "failed");
    const retryAfter = numberFromResultData(data, "retry_after_seconds");
    const parts = [
      submitted != null ? `${submitted} submitted` : null,
      skipped != null ? `${skipped} skipped` : null,
      invalid != null && invalid > 0 ? `${invalid} refused by the provider` : null,
      failed != null ? `${failed} failed` : null,
    ].filter(Boolean);
    if (parts.length > 0) {
      return `Last result: ${parts.join(", ")}${retryAfter != null && retryAfter > 0 ? ". Provider rate limit reached during this run." : ""}`;
    }
  }

  return `Last run: ${formatDateTime(result.completed_at)}`;
}

function TaskActionRow({
  task,
  fallbackName,
  fallbackDescription,
  onRun,
  pending,
}: {
  task: TaskInfo | undefined;
  fallbackName: string;
  fallbackDescription: string;
  onRun: () => void;
  pending: boolean;
}) {
  const key = task?.key;
  const running = task?.state === "running" || task?.state === "cancelling";

  return (
    <div className="border-border flex flex-col gap-3 border-b py-4 last:border-b-0 sm:flex-row sm:items-center">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="text-sm font-semibold">{task?.name ?? fallbackName}</h3>
          {task?.last_execution && <TaskStatusBadge result={task.last_execution} />}
        </div>
        <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
          {task?.description ?? fallbackDescription}
        </p>
        <p className="text-muted-foreground mt-1 text-xs">{formatTaskResult(task)}</p>
      </div>

      <div className="flex flex-col gap-2 sm:flex-row">
        {key && (
          <Button variant="outline" size="sm" asChild>
            <Link to={`/admin/tasks/${key}`}>History</Link>
          </Button>
        )}
        <Button type="button" size="sm" onClick={onRun} disabled={!task || running || pending}>
          {pending ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Play className="h-3.5 w-3.5" />
          )}
          {running ? "Running" : "Run now"}
        </Button>
      </div>
    </div>
  );
}

/** Run-now shortcuts for marker tasks, with their last result. */
export function MarkerTasksCard() {
  useEventChannel("tasks");
  const { data: tasks } = useTasks();
  const runTask = useRunTask();
  // Per-task, not one shared value: tasks can be started back to back,
  // and the first completion must not re-enable the row that is still running.
  const [pendingTasks, setPendingTasks] = useState<ReadonlySet<string>>(new Set());

  const syncTask = tasks?.find((task) => task.key === "sync_markers");
  const detectTask = tasks?.find((task) => task.key === "detect_intro_markers");
  const contributeTask = tasks?.find((task) => task.key === "contribute_markers");

  async function run(key: string) {
    setPendingTasks((current) => new Set(current).add(key));
    try {
      await runTask.mutateAsync(key);
    } finally {
      setPendingTasks((current) => {
        const next = new Set(current);
        next.delete(key);
        return next;
      });
    }
  }

  return (
    <div className="max-w-2xl">
      <TaskActionRow
        task={syncTask}
        fallbackName="Sync online markers"
        fallbackDescription="Fetches missing markers from enabled online providers and refreshes saved markers. Requires Save to library."
        onRun={() => void run("sync_markers")}
        pending={pendingTasks.has("sync_markers")}
      />
      <TaskActionRow
        task={detectTask}
        fallbackName="Detect markers on this server"
        fallbackDescription="Analyzes files for intros in libraries with marker detection enabled."
        onRun={() => void run("detect_intro_markers")}
        pending={pendingTasks.has("detect_intro_markers")}
      />
      <TaskActionRow
        task={contributeTask}
        fallbackName="Share intro markers"
        fallbackDescription="Sends eligible intros detected on this server to providers with automatic sharing enabled."
        onRun={() => void run("contribute_markers")}
        pending={pendingTasks.has("contribute_markers")}
      />
    </div>
  );
}
