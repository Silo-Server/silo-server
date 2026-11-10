import { useState } from "react";
import { Link } from "react-router";
import { Loader2, Play } from "lucide-react";

import type { TaskInfo } from "@/api/types";
import { TaskStatusBadge } from "@/components/admin/TaskStatusBadge";
import { useEventChannel } from "@/components/realtimeEventsContext";
import { Button } from "@/components/ui/button";
import { useTasks, useRunTask } from "@/hooks/queries/admin/tasks";
import { formatDateTime } from "@/lib/datetime";
import { useT, type Translate } from "@/i18n";

function numberFromResultData(data: Record<string, unknown> | undefined, key: string) {
  const value = data?.[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function formatTaskResult(t: Translate, task: TaskInfo | undefined) {
  const result = task?.last_execution;
  if (!result) return t("web.admin.settings.markers.tasks.neverRun");

  const data = result.result_data;
  if (task.key === "contribute_markers") {
    const submitted = numberFromResultData(data, "submitted");
    const skipped = numberFromResultData(data, "skipped");
    const invalid = numberFromResultData(data, "invalid");
    const failed = numberFromResultData(data, "failed");
    const retryAfter = numberFromResultData(data, "retry_after_seconds");
    const count = (n: number) => ({ count: n, count_text: n });
    const parts = [
      submitted != null ? t("web.admin.settings.markers.tasks.submitted", count(submitted)) : null,
      skipped != null ? t("web.admin.settings.markers.tasks.skipped", count(skipped)) : null,
      invalid != null && invalid > 0
        ? t("web.admin.settings.markers.tasks.refused", count(invalid))
        : null,
      failed != null ? t("web.admin.settings.markers.tasks.failed", count(failed)) : null,
    ].filter(Boolean);
    if (parts.length > 0) {
      const list = parts.join(t("web.admin.settings.markers.tasks.resultSeparator"));
      return retryAfter != null && retryAfter > 0
        ? t("web.admin.settings.markers.tasks.lastResultRateLimited", { parts: list })
        : t("web.admin.settings.markers.tasks.lastResult", { parts: list });
    }
  }

  return t("web.admin.settings.markers.tasks.lastRun", {
    date: formatDateTime(result.completed_at),
  });
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
  const t = useT();
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
        <p className="text-muted-foreground mt-1 text-xs">{formatTaskResult(t, task)}</p>
      </div>

      <div className="flex flex-col gap-2 sm:flex-row">
        {key && (
          <Button variant="outline" size="sm" asChild>
            <Link to={`/admin/tasks/${key}`}>{t("web.admin.settings.markers.tasks.history")}</Link>
          </Button>
        )}
        <Button type="button" size="sm" onClick={onRun} disabled={!task || running || pending}>
          {pending ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Play className="h-3.5 w-3.5" />
          )}
          {running
            ? t("web.admin.settings.markers.tasks.running")
            : t("web.admin.settings.markers.tasks.runNow")}
        </Button>
      </div>
    </div>
  );
}

/** Run-now shortcuts for marker tasks, with their last result. */
export function MarkerTasksCard() {
  const t = useT();
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
        fallbackName={t("web.admin.settings.markers.tasks.syncName")}
        fallbackDescription={t("web.admin.settings.markers.tasks.syncDescription")}
        onRun={() => void run("sync_markers")}
        pending={pendingTasks.has("sync_markers")}
      />
      <TaskActionRow
        task={detectTask}
        fallbackName={t("web.admin.settings.markers.tasks.detectName")}
        fallbackDescription={t("web.admin.settings.markers.tasks.detectDescription")}
        onRun={() => void run("detect_intro_markers")}
        pending={pendingTasks.has("detect_intro_markers")}
      />
      <TaskActionRow
        task={contributeTask}
        fallbackName={t("web.admin.settings.markers.tasks.contributeName")}
        fallbackDescription={t("web.admin.settings.markers.tasks.contributeDescription")}
        onRun={() => void run("contribute_markers")}
        pending={pendingTasks.has("contribute_markers")}
      />
    </div>
  );
}
