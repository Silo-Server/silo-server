import { useState } from "react";
import { useParams, Link } from "react-router";
import { Play, Square, Plus, Trash2, ChevronRight, ChevronDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { TaskStatusBadge } from "@/components/admin/TaskStatusBadge";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  useTask,
  useTaskHistory,
  useRunTask,
  useCancelTask,
  useUpdateTriggers,
  useTaskMetrics,
  type MetadataRefreshMetrics,
} from "@/hooks/queries/admin/tasks";
import type { ExecutionResult, TriggerConfig, TriggerType } from "@/api/types";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";

const REFRESH_REASON_LABELS: Record<string, string> = {
  episode_incomplete: "Episode incomplete",
  stale_provider_id: "Stale provider ID",
  refresh_failure: "Refresh failure",
  core_metadata_incomplete: "Core metadata incomplete",
};

// --- Trigger display helpers ---

function describeTrigger(t: TriggerConfig): string {
  switch (t.type) {
    case "interval": {
      const ms = t.interval_ms ?? 0;
      if (ms >= 3_600_000) return `Every ${Math.round(ms / 3_600_000)} hour(s)`;
      if (ms >= 60_000) return `Every ${Math.round(ms / 60_000)} minute(s)`;
      return `Every ${Math.round(ms / 1000)} second(s)`;
    }
    case "daily":
      return `Daily at ${t.time_of_day ?? "00:00"}`;
    case "weekly": {
      const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
      return `${days[t.day_of_week ?? 0]} at ${t.time_of_day ?? "00:00"}`;
    }
    case "startup":
      return "On server startup";
    default:
      return t.type;
  }
}

function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const totalSeconds = Math.floor(ms / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const remainSec = totalSeconds - minutes * 60;
  return `${minutes}m ${remainSec}s`;
}

function formatDateTime(dateStr: string): string {
  return formatPreferredDateTime(dateStr);
}

function formatOptionalDateTime(dateStr?: string | null): string {
  return dateStr ? formatDateTime(dateStr) : "—";
}

function RefreshMetricsPanel({ metrics }: { metrics: MetadataRefreshMetrics }) {
  const reasonCounts = metrics.reason_counts.filter((entry) => entry.count > 0);

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            Refresh Backlog
          </div>
          <div className="mt-2 text-2xl font-semibold">{metrics.total}</div>
        </div>
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            Due for Refresh
          </div>
          <div className="mt-2 text-2xl font-semibold">{metrics.due}</div>
        </div>
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            Processing
          </div>
          <div className="mt-2 text-2xl font-semibold">{metrics.leased}</div>
        </div>
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            Waiting Since
          </div>
          <div className="mt-2 text-sm font-medium">
            {formatOptionalDateTime(metrics.oldest_due_at)}
          </div>
        </div>
        <div className="surface-panel rounded-2xl border-0 p-4">
          <div className="text-muted-foreground text-xs tracking-[0.16em] uppercase">
            Next Claim Timeout
          </div>
          <div className="mt-2 text-sm font-medium">
            {formatOptionalDateTime(metrics.oldest_lease_expires_at)}
          </div>
        </div>
      </div>

      <div className="grid gap-4 xl:grid-cols-[1.2fr_0.8fr]">
        <div className="surface-panel rounded-2xl border-0 p-4">
          <h3 className="text-sm font-medium">Reason breakdown</h3>
          <div className="mt-3 flex flex-wrap gap-2">
            {reasonCounts.length === 0 && (
              <span className="text-muted-foreground text-sm">No queued debt.</span>
            )}
            {reasonCounts.map((entry) => (
              <Badge key={entry.reason} variant="secondary">
                {REFRESH_REASON_LABELS[entry.reason] ?? entry.reason}: {entry.count}
              </Badge>
            ))}
          </div>
          <div className="mt-4 grid gap-2 sm:grid-cols-5">
            {metrics.attempt_buckets.map((bucket) => (
              <div key={bucket.label} className="bg-muted/30 rounded-xl p-3">
                <div className="text-muted-foreground text-xs">Attempts {bucket.label}</div>
                <div className="mt-1 text-lg font-semibold">{bucket.count}</div>
              </div>
            ))}
          </div>
        </div>

        <div className="surface-panel rounded-2xl border-0 p-4">
          <h3 className="text-sm font-medium">Recent errors</h3>
          <div className="mt-3 space-y-3">
            {metrics.recent_errors.length === 0 && (
              <p className="text-muted-foreground text-sm">No recent queue errors.</p>
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
                      {entry.type || "item"} · attempts {entry.attempt_count}
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
          <h3 className="text-sm font-medium">Due samples</h3>
        </div>
        <table className="w-full text-sm">
          <thead>
            <tr className="border-border bg-muted/40 border-b">
              <th className="px-4 py-2 text-left font-medium">Item</th>
              <th className="px-4 py-2 text-left font-medium">Next refresh</th>
              <th className="px-4 py-2 text-left font-medium">Attempts</th>
              <th className="px-4 py-2 text-left font-medium">Last attempt</th>
            </tr>
          </thead>
          <tbody>
            {metrics.due_samples.length === 0 && (
              <tr>
                <td colSpan={4} className="text-muted-foreground px-4 py-4 text-center">
                  No due items right now.
                </td>
              </tr>
            )}
            {metrics.due_samples.map((entry) => (
              <tr key={entry.content_id} className="border-border border-b last:border-b-0">
                <td className="px-4 py-2">
                  <div className="font-medium">{entry.title || entry.content_id}</div>
                  <div className="text-muted-foreground text-xs">{entry.type || "item"}</div>
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

// --- Trigger form ---

const TRIGGER_TYPES: { value: TriggerType; label: string }[] = [
  { value: "interval", label: "Interval" },
  { value: "daily", label: "Daily" },
  { value: "weekly", label: "Weekly" },
  { value: "startup", label: "On Startup" },
];

const DAYS_OF_WEEK = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

interface TriggerFormRowProps {
  trigger: TriggerConfig;
  onChange: (updated: TriggerConfig) => void;
  onRemove: () => void;
}

function TriggerFormRow({ trigger, onChange, onRemove }: TriggerFormRowProps) {
  // Convert interval_ms to a user-friendly value + unit
  const intervalToDisplay = (ms: number): { value: number; unit: string } => {
    if (ms >= 3_600_000 && ms % 3_600_000 === 0) return { value: ms / 3_600_000, unit: "hours" };
    if (ms >= 60_000 && ms % 60_000 === 0) return { value: ms / 60_000, unit: "minutes" };
    return { value: ms / 1000, unit: "seconds" };
  };

  const displayToMs = (value: number, unit: string): number => {
    if (unit === "hours") return value * 3_600_000;
    if (unit === "minutes") return value * 60_000;
    return value * 1000;
  };

  const intervalDisplay = intervalToDisplay(trigger.interval_ms ?? 60_000);
  const [intervalUnit, setIntervalUnit] = useState(intervalDisplay.unit);

  return (
    <div className="surface-panel-subtle flex flex-col gap-3 rounded-xl p-3 sm:flex-row sm:items-start">
      <div className="flex-1 space-y-2">
        <Select
          value={trigger.type}
          onValueChange={(v) => onChange({ ...trigger, type: v as TriggerType })}
        >
          <SelectTrigger className="w-full sm:w-[160px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {TRIGGER_TYPES.map((tt) => (
              <SelectItem key={tt.value} value={tt.value}>
                {tt.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        {trigger.type === "interval" && (
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Input
              type="number"
              min={1}
              className="w-full sm:w-[100px]"
              value={intervalDisplay.value}
              onChange={(e) => {
                const val = parseInt(e.target.value, 10) || 1;
                onChange({ ...trigger, interval_ms: displayToMs(val, intervalUnit) });
              }}
            />
            <Select
              value={intervalUnit}
              onValueChange={(u) => {
                setIntervalUnit(u);
                onChange({
                  ...trigger,
                  interval_ms: displayToMs(intervalDisplay.value, u),
                });
              }}
            >
              <SelectTrigger className="w-full sm:w-[120px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="seconds">Seconds</SelectItem>
                <SelectItem value="minutes">Minutes</SelectItem>
                <SelectItem value="hours">Hours</SelectItem>
              </SelectContent>
            </Select>
          </div>
        )}

        {trigger.type === "daily" && (
          <Input
            type="time"
            className="w-full sm:w-[140px]"
            value={trigger.time_of_day ?? "00:00"}
            onChange={(e) => onChange({ ...trigger, time_of_day: e.target.value })}
          />
        )}

        {trigger.type === "weekly" && (
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Select
              value={String(trigger.day_of_week ?? 0)}
              onValueChange={(v) => onChange({ ...trigger, day_of_week: parseInt(v, 10) })}
            >
              <SelectTrigger className="w-full sm:w-[140px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {DAYS_OF_WEEK.map((day, i) => (
                  <SelectItem key={i} value={String(i)}>
                    {day}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Input
              type="time"
              className="w-full sm:w-[140px]"
              value={trigger.time_of_day ?? "00:00"}
              onChange={(e) => onChange({ ...trigger, time_of_day: e.target.value })}
            />
          </div>
        )}

        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <label className="text-muted-foreground text-xs whitespace-nowrap">Max runtime:</label>
          <Input
            type="number"
            min={0}
            className="w-full sm:w-[100px]"
            placeholder="None"
            value={trigger.max_runtime_ms ? trigger.max_runtime_ms / 60_000 : ""}
            onChange={(e) => {
              const val = parseInt(e.target.value, 10);
              onChange({
                ...trigger,
                max_runtime_ms: val > 0 ? val * 60_000 : undefined,
              });
            }}
          />
          <span className="text-muted-foreground text-xs">minutes</span>
        </div>
      </div>

      <Button
        variant="ghost"
        size="icon"
        onClick={onRemove}
        className="shrink-0 self-end sm:self-auto"
      >
        <Trash2 className="h-4 w-4" />
      </Button>
    </div>
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
  const hasResultData = result.result_data && Object.keys(result.result_data).length > 0;

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
        <td className="px-4 py-2">{formatDuration(result.duration_ms)}</td>
        <td className="px-4 py-2">
          <TaskStatusBadge result={result} />
        </td>
        <td className="text-muted-foreground max-w-xs truncate px-4 py-2">
          {result.error_message || "—"}
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
  const { key } = useParams<{ key: string }>();
  const { data: task, isLoading } = useTask(key!);
  const { data: history } = useTaskHistory(key!);
  const { data: metrics } = useTaskMetrics(key!);
  const runTask = useRunTask();
  const cancelTask = useCancelTask();
  const updateTriggers = useUpdateTriggers();

  const [editing, setEditing] = useState(false);
  const [editTriggers, setEditTriggers] = useState<TriggerConfig[]>([]);
  const [expandedRows, setExpandedRows] = useState<Set<number>>(new Set());

  const toggleRow = (id: number) => {
    setExpandedRows((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  if (isLoading || !task) {
    return <p className="page-shell text-muted-foreground py-8 text-sm">Loading...</p>;
  }

  const isRunning = task.state === "running" || task.state === "cancelling";

  const startEditing = () => {
    setEditTriggers(task.triggers.map((t) => ({ ...t })));
    setEditing(true);
  };

  const saveTriggers = () => {
    updateTriggers.mutate(
      { key: task.key, triggers: editTriggers },
      {
        onSuccess: () => setEditing(false),
      },
    );
  };

  const addTrigger = () => {
    setEditTriggers([...editTriggers, { type: "interval", interval_ms: 3_600_000 }]);
  };

  const removeTrigger = (index: number) => {
    setEditTriggers(editTriggers.filter((_, i) => i !== index));
  };

  const updateTrigger = (index: number, updated: TriggerConfig) => {
    setEditTriggers(editTriggers.map((t, i) => (i === index ? updated : t)));
  };

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <nav
        aria-label="Breadcrumb"
        className="text-muted-foreground flex items-center gap-1.5 text-sm"
      >
        <Link to="/admin" className="hover:text-foreground transition-colors">
          Admin
        </Link>
        <ChevronRight className="h-3.5 w-3.5" />
        <Link to="/admin/tasks" className="hover:text-foreground transition-colors">
          Scheduled Tasks
        </Link>
        <ChevronRight className="h-3.5 w-3.5" />
        <span className="text-foreground font-medium">{task.name}</span>
      </nav>

      <div className="page-header gap-5">
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">{task.name}</h1>
            <Badge variant="outline">{task.category}</Badge>
          </div>
          <p className="page-subtitle text-sm sm:text-base">{task.description}</p>
        </div>

        {isRunning ? (
          <Button
            variant="outline"
            className="w-full sm:w-auto"
            onClick={() => cancelTask.mutate(task.key)}
            disabled={cancelTask.isPending || task.state === "cancelling"}
          >
            <Square className="mr-1.5 h-4 w-4" />
            Cancel
          </Button>
        ) : (
          <Button
            variant="outline"
            className="w-full sm:w-auto"
            onClick={() => runTask.mutate(task.key)}
            disabled={runTask.isPending}
          >
            <Play className="mr-1.5 h-4 w-4" />
            Run Now
          </Button>
        )}
      </div>

      {task.key === "scan_libraries" && (
        <div className="surface-panel-subtle rounded-xl p-4 text-sm leading-relaxed">
          <span className="text-muted-foreground">
            This task history records how long it took to queue per-library scan runs. Actual scan
            work continues in the background and is tracked from{" "}
          </span>
          <Link to="/admin/libraries" className="text-foreground hover:text-primary font-medium">
            Admin Libraries
          </Link>
          <span className="text-muted-foreground"> and Server Activity.</span>
        </div>
      )}

      {isRunning && (
        <div className="surface-panel-subtle space-y-1 rounded-xl p-4">
          <div className="bg-muted h-2.5 w-full overflow-hidden rounded-full">
            <div
              className={`h-full rounded-full transition-all duration-300 ${
                task.state === "cancelling" ? "bg-yellow-500" : "bg-primary"
              }`}
              style={{ width: `${Math.max(task.progress, 2)}%` }}
            />
          </div>
          <p className="text-muted-foreground text-sm">
            {task.state === "cancelling"
              ? "Cancelling..."
              : task.progress_message || `${Math.round(task.progress)}%`}
          </p>
        </div>
      )}

      {task.key === "refresh_metadata" && metrics && (
        <div className="space-y-3">
          <h2 className="text-lg font-medium tracking-tight">Queue health</h2>
          <RefreshMetricsPanel metrics={metrics} />
        </div>
      )}

      <div className="space-y-3">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <h2 className="text-lg font-medium tracking-tight">Schedule</h2>
          {!editing && (
            <Button variant="outline" size="sm" onClick={startEditing}>
              Edit Schedule
            </Button>
          )}
        </div>

        {!editing ? (
          <div className="surface-panel overflow-hidden rounded-2xl border-0">
            {task.triggers.length === 0 && (
              <p className="text-muted-foreground p-4 text-sm">No triggers configured.</p>
            )}
            {task.triggers.map((trigger, i) => (
              <div key={i} className="border-border border-b px-4 py-2.5 text-sm last:border-b-0">
                {describeTrigger(trigger)}
              </div>
            ))}
          </div>
        ) : (
          <div className="surface-panel rounded-2xl border-0 p-4">
            <div className="space-y-3">
              {editTriggers.map((trigger, i) => (
                <TriggerFormRow
                  key={i}
                  trigger={trigger}
                  onChange={(updated) => updateTrigger(i, updated)}
                  onRemove={() => removeTrigger(i)}
                />
              ))}

              <Button variant="outline" size="sm" onClick={addTrigger}>
                <Plus className="mr-1.5 h-3.5 w-3.5" />
                Add Trigger
              </Button>

              <div className="flex flex-col gap-2 sm:flex-row">
                <Button size="sm" onClick={saveTriggers} disabled={updateTriggers.isPending}>
                  Save
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setEditing(false)}>
                  Cancel
                </Button>
              </div>
            </div>
          </div>
        )}
      </div>

      <div className="space-y-3">
        <h2 className="text-lg font-medium tracking-tight">Execution history</h2>

        <div className="surface-panel overflow-hidden rounded-2xl border-0">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-border bg-muted/50 border-b">
                <th className="px-4 py-2 text-left font-medium">Started</th>
                <th className="px-4 py-2 text-left font-medium">Duration</th>
                <th className="px-4 py-2 text-left font-medium">Status</th>
                <th className="px-4 py-2 text-left font-medium">Error</th>
              </tr>
            </thead>
            <tbody>
              {(!history || history.length === 0) && (
                <tr>
                  <td colSpan={4} className="text-muted-foreground px-4 py-4 text-center">
                    No execution history.
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
      </div>
    </div>
  );
}
