import { useState, useMemo, useEffect, useRef } from "react";
import { Link, useLocation } from "react-router";
import { Popover as PopoverPrimitive } from "radix-ui";
import { Activity, ChevronRight, Loader, ScanLine } from "lucide-react";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import { useTasks } from "@/hooks/queries/admin/tasks";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  useRealtimeEvents,
  type RealtimeConnectionState,
} from "@/components/realtimeEventsContext";
import type { TaskInfo, ScanRun } from "@/api/types";

const CONNECTION_PROBLEM_INDICATOR_DELAY_MS = 4_000;

interface ServerActivityProps {
  /** Hide the trigger button entirely when there is no activity */
  hideWhenEmpty?: boolean;
}

// ── Data hook ────────────────────────────────────────────────
// Channel subscriptions are owned by the parent layout
// (AdminLayoutEventChannels / AdminEventChannels), not here.
function useServerActivityData() {
  const { data: sessions = [] } = useAdminSessions();
  const { data: tasks = [] } = useTasks();
  const { data: scans } = useActiveScans();
  const { data: libraries = [] } = useAdminLibraries();
  const { connectionState } = useRealtimeEvents();

  const activeScans = useMemo(
    () => (scans ?? []).filter((s) => s.status === "accepted" || s.status === "running"),
    [scans],
  );

  const runningTasks = useMemo(() => tasks.filter((t) => t.state === "running"), [tasks]);

  const streamCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const s of sessions) {
      const method = s.play_method || "unknown";
      counts[method] = (counts[method] || 0) + 1;
    }
    return counts;
  }, [sessions]);

  const totalActive = sessions.length + runningTasks.length + activeScans.length;

  const libraryName = (id: number) => libraries.find((l) => l.id === id)?.name ?? `Library #${id}`;

  return {
    sessions,
    runningTasks,
    activeScans,
    streamCounts,
    totalActive,
    libraryName,
    connectionState,
    scansLoaded: scans !== undefined,
  };
}

// ── Main component ───────────────────────────────────────────

function useDelayedConnectionProblem(connectionState: RealtimeConnectionState) {
  const isNonLive = connectionState !== "live";
  const previousIsNonLiveRef = useRef(false);
  const [connectionProblemState, setConnectionProblemState] = useState(false);

  useEffect(() => {
    const wasNonLive = previousIsNonLiveRef.current;
    previousIsNonLiveRef.current = isNonLive;

    if (!isNonLive) {
      setConnectionProblemState(false);
      return;
    }

    if (wasNonLive) {
      return;
    }

    const timeoutID = window.setTimeout(
      () => setConnectionProblemState(true),
      CONNECTION_PROBLEM_INDICATOR_DELAY_MS,
    );
    return () => window.clearTimeout(timeoutID);
  }, [isNonLive]);

  return isNonLive && connectionProblemState;
}

export default function ServerActivity({ hideWhenEmpty = false }: ServerActivityProps) {
  const [open, setOpen] = useState(false);
  const location = useLocation();

  const {
    sessions,
    runningTasks,
    activeScans,
    streamCounts,
    totalActive,
    libraryName,
    connectionState,
    scansLoaded,
  } = useServerActivityData();
  const showConnectionProblem = useDelayedConnectionProblem(connectionState);

  // Keep mounted while popover is open so Radix can animate closed
  if (hideWhenEmpty && totalActive === 0 && !open) return null;

  return (
    <PopoverPrimitive.Root key={location.pathname} open={open} onOpenChange={setOpen}>
      <PopoverPrimitive.Trigger asChild>
        <button
          type="button"
          aria-label={
            totalActive > 0 ? `Server activity: ${totalActive} active` : "Server activity"
          }
          className="hover:bg-accent/60 relative flex h-9 w-9 items-center justify-center rounded-xl transition-colors"
        >
          <Activity
            className={`h-[18px] w-[18px] ${
              totalActive > 0 ? "text-primary" : "text-muted-foreground"
            }`}
          />
          {totalActive > 0 && (
            <span className="absolute -top-0.5 -right-0.5 flex h-[18px] min-w-[18px] animate-[pulse-opacity_2s_ease-in-out_infinite] items-center justify-center rounded-full bg-red-500 px-1 text-[10px] font-bold text-white shadow-sm">
              {totalActive}
            </span>
          )}
          {showConnectionProblem && (
            <span
              className="absolute -right-0.5 -bottom-0.5 h-2.5 w-2.5 rounded-full bg-red-500 ring-2 ring-background"
              aria-hidden="true"
            />
          )}
        </button>
      </PopoverPrimitive.Trigger>

      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          side="bottom"
          align="end"
          sideOffset={8}
          className="data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 z-50 w-[320px] origin-[var(--radix-popover-content-transform-origin)] rounded-2xl border border-[color-mix(in_srgb,var(--border)_72%,transparent)] bg-[color-mix(in_srgb,var(--surface)_95%,transparent)] p-0 shadow-[0_24px_50px_-12px_rgb(0_0_0/0.5)] backdrop-blur-2xl"
        >
          {/* Header */}
          <div className="flex items-center justify-between border-b border-[color-mix(in_srgb,var(--border)_50%,transparent)] px-4 py-3">
            <span className="text-[13px] font-bold">Server Activity</span>
            {connectionState !== "live" && (
              <span className="text-warning text-[10px] font-medium">
                {connectionState === "connecting" ? "Connecting…" : "Disconnected"}
              </span>
            )}
          </div>

          <div className="max-h-[400px] overflow-y-auto">
            {totalActive === 0 && scansLoaded ? (
              <div className="text-muted-foreground px-4 py-8 text-center text-sm">
                No active server activity
              </div>
            ) : (
              <>
                {/* Streams */}
                <ActivitySection
                  title="Streams"
                  count={sessions.length}
                  href="/admin/activity"
                  onNavigate={() => setOpen(false)}
                >
                  {sessions.length > 0 ? (
                    <div className="space-y-1.5">
                      {Object.entries(streamCounts).map(([method, count]) => (
                        <StreamCountRow key={method} method={method} count={count} />
                      ))}
                    </div>
                  ) : (
                    <EmptyRow>No active streams</EmptyRow>
                  )}
                </ActivitySection>

                {/* Tasks */}
                <ActivitySection
                  title="Tasks"
                  count={runningTasks.length}
                  href="/admin/tasks"
                  onNavigate={() => setOpen(false)}
                >
                  {runningTasks.length > 0 ? (
                    <div className="space-y-2">
                      {runningTasks.map((task) => (
                        <TaskRow key={task.key} task={task} />
                      ))}
                    </div>
                  ) : (
                    <EmptyRow>No running tasks</EmptyRow>
                  )}
                </ActivitySection>

                {/* Scans */}
                <ActivitySection
                  title="Scans"
                  count={activeScans.length}
                  href="/admin/libraries"
                  onNavigate={() => setOpen(false)}
                  last
                >
                  {activeScans.length > 0 ? (
                    <div className="space-y-1.5">
                      {activeScans.map((scan) => (
                        <ScanRow
                          key={scan.id}
                          scan={scan}
                          libraryName={libraryName(scan.library_id)}
                        />
                      ))}
                    </div>
                  ) : (
                    <EmptyRow>No active scans</EmptyRow>
                  )}
                </ActivitySection>
              </>
            )}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  );
}

// ── Sub-components ───────────────────────────────────────────

function ActivitySection({
  title,
  count,
  href,
  onNavigate,
  last = false,
  children,
}: {
  title: string;
  count: number;
  href: string;
  onNavigate: () => void;
  last?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div
      className={
        last
          ? "px-4 py-3"
          : "border-b border-[color-mix(in_srgb,var(--border)_40%,transparent)] px-4 py-3"
      }
    >
      <div className="mb-2 flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-muted-foreground text-[10px] font-semibold tracking-[0.12em] uppercase">
            {title}
          </span>
          {count > 0 && (
            <span className="bg-primary/10 text-primary rounded-md px-1.5 py-0.5 text-[10px] leading-none font-bold">
              {count}
            </span>
          )}
        </div>
        <Link
          to={href}
          onClick={onNavigate}
          className="text-muted-foreground hover:text-primary flex items-center gap-0.5 text-[10px] font-medium transition-colors"
        >
          View all
          <ChevronRight className="h-3 w-3" />
        </Link>
      </div>
      {children}
    </div>
  );
}

const METHOD_META: Record<string, { label: string; color: string }> = {
  direct: { label: "Direct Play", color: "bg-success" },
  remux: { label: "Remux", color: "bg-info" },
  transcode: { label: "Transcode", color: "bg-warning" },
};

function StreamCountRow({ method, count }: { method: string; count: number }) {
  const { label = method, color = "bg-muted-foreground" } = METHOD_META[method] ?? {};
  return (
    <div className="flex items-center gap-2.5">
      <span className={`h-2 w-2 rounded-full ${color}`} />
      <span className="text-[12px] font-medium">
        {count} {label}
      </span>
    </div>
  );
}

function TaskRow({ task }: { task: TaskInfo }) {
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between">
        <span className="truncate text-[12px] font-medium">{task.name}</span>
        <span className="text-muted-foreground ml-2 shrink-0 text-[10px] font-semibold tabular-nums">
          {Math.round(task.progress)}%
        </span>
      </div>
      <div className="bg-muted h-1.5 overflow-hidden rounded-full">
        <div
          className="bg-primary h-full rounded-full transition-[width] duration-300 ease-out"
          style={{ width: `${Math.min(100, Math.max(0, task.progress))}%` }}
        />
      </div>
      {task.progress_message && (
        <div className="text-muted-foreground truncate text-[10px]">{task.progress_message}</div>
      )}
    </div>
  );
}

function ScanRow({ scan, libraryName }: { scan: ScanRun; libraryName: string }) {
  const progressLabel = formatScanProgress(scan);
  return (
    <div className="flex items-start gap-2.5">
      {scan.status === "running" ? (
        <Loader className="text-primary mt-0.5 h-3.5 w-3.5 animate-spin" />
      ) : (
        <ScanLine className="text-muted-foreground mt-0.5 h-3.5 w-3.5" />
      )}
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="flex items-center gap-2">
          <span className="truncate text-[12px] font-medium">{libraryName}</span>
          <span className="text-muted-foreground ml-auto shrink-0 text-[10px]">
            {scan.status === "running" ? "Scanning…" : "Queued"}
          </span>
        </div>
        <div className="text-muted-foreground truncate text-[10px]">
          {formatScanLabel(scan)}
          {scan.path ? ` · ${scan.path}` : ""}
        </div>
        {progressLabel && (
          <div className="text-muted-foreground/80 truncate text-[10px]">{progressLabel}</div>
        )}
      </div>
    </div>
  );
}

function formatScanLabel(scan: ScanRun) {
  switch (scan.mode) {
    case "library":
      return "Full library";
    case "subtree":
      return "Subtree";
    case "file":
      return "Single file";
    default:
      return scan.mode;
  }
}

function formatScanProgress(scan: ScanRun) {
  const result = scan.result;
  if (!result) {
    return null;
  }
  if (result.total_files && result.files_processed) {
    const percent = Math.max(
      0,
      Math.min(100, Math.round((result.files_processed / result.total_files) * 100)),
    );
    return `${result.message ?? "Processing files"} · ${result.files_processed.toLocaleString()} / ${result.total_files.toLocaleString()} (${percent}%)`;
  }
  if (result.message) {
    return result.message;
  }
  return null;
}

function EmptyRow({ children }: { children: React.ReactNode }) {
  return <div className="text-muted-foreground py-1 text-[11px]">{children}</div>;
}
