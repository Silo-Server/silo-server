import { useState, useMemo, useEffect, useRef } from "react";
import { Link, useLocation } from "react-router";
import { Popover as PopoverPrimitive } from "radix-ui";
import {
  Activity,
  AlertTriangle,
  ChevronRight,
  Clock,
  Loader,
  Pause,
  ScanLine,
} from "lucide-react";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import { useTasksIncludingHidden } from "@/hooks/queries/admin/tasks";
import { useActiveScans } from "@/hooks/queries/admin/scans";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminDownloadPreparations } from "@/hooks/queries/admin/downloadPreparations";
import type { AdminDownloadPreparation } from "@/api/v2/adminDownloadPreparations";
import {
  useRealtimeEvents,
  type RealtimeConnectionState,
} from "@/components/realtimeEventsContext";
import type { TaskInfo, ScanRun } from "@/api/types";
import {
  activityMethodMeta,
  classifyActivityMethod,
  compareActivityMethods,
} from "@/pages/adminActivityPresentation";
import { cn } from "@/lib/utils";
import { clampTaskProgress, formatTaskProgress } from "@/lib/taskProgress";
import {
  activePreparationCount,
  formatPreparationKind,
  formatRemaining,
  preparationCompactTitle,
  preparationPercent,
  preparationRemainingSeconds,
  preparationRunningLabel,
  preparationWorker,
} from "@/pages/adminDownloadPreparationPresentation";
import { formatLocale } from "@/lib/appLocale";
import { useT, type MessageKey, type Translate } from "@/i18n";

const CONNECTION_PROBLEM_INDICATOR_DELAY_MS = 4_000;
const MAX_ACTIVITY_SCAN_ROWS = 25;
const MAX_BADGE_COUNT = 99;
const ACTIVE_SCAN_SNAPSHOT_LIMIT = 500;
const MAX_ACTIVITY_PREPARATION_ROWS = 3;
// Joins a scan's mode and its path, which is data.
const DETAIL_SEPARATOR = " · ";
export const PREPARATIONS_HREF = "/admin/downloads?tab=preparation";

interface ServerActivityProps {
  /** Hide the trigger button entirely when there is no activity */
  hideWhenEmpty?: boolean;
  className?: string;
}

// ── Data hook ────────────────────────────────────────────────
// Channel subscriptions are owned by the parent layout
// (AdminLayoutEventChannels / AdminEventChannels), not here.
function useServerActivityData() {
  const t = useT();
  const { data: sessions = [] } = useAdminSessions();
  const { data: tasks = [] } = useTasksIncludingHidden();
  const { data: scans } = useActiveScans();
  const { data: libraries = [] } = useAdminLibraries();
  // Undefined when the server has no preparation queue (the read answers 503);
  // the section is hidden rather than showing an error.
  const { data: preparations } = useAdminDownloadPreparations();
  const { connectionState } = useRealtimeEvents();

  const activeScans = useMemo(
    () => (scans ?? []).filter((s) => s.status === "accepted" || s.status === "running"),
    [scans],
  );

  const runningTasks = useMemo(() => tasks.filter((t) => t.state === "running"), [tasks]);

  const streamCounts = useMemo(() => {
    const counts: Record<string, number> = {};
    for (const s of sessions) {
      const method = classifyActivityMethod(s);
      counts[method] = (counts[method] || 0) + 1;
    }
    return counts;
  }, [sessions]);

  const runningPreparations = useMemo(
    () => (preparations?.items ?? []).filter((p) => p.state === "running"),
    [preparations],
  );
  const preparationCounts = preparations?.counts;
  // Queued jobs count as activity, as queued scans already do.
  const activePreparations = activePreparationCount(preparationCounts);

  const totalActive =
    sessions.length + runningTasks.length + activeScans.length + activePreparations;

  const libraryName = (id: number) =>
    libraries.find((l) => l.id === id)?.name ??
    t("web.admin.common.serverActivity.libraryFallback", { id: String(id) });

  return {
    sessions,
    runningTasks,
    activeScans,
    streamCounts,
    totalActive,
    libraryName,
    connectionState,
    scansLoaded: scans !== undefined,
    preparationCounts,
    runningPreparations,
    activePreparations,
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

export default function ServerActivity({ hideWhenEmpty = false, className }: ServerActivityProps) {
  const t = useT();
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
    preparationCounts,
    runningPreparations,
    activePreparations,
  } = useServerActivityData();
  const showConnectionProblem = useDelayedConnectionProblem(connectionState);
  const visibleActiveScans = activeScans.slice(0, MAX_ACTIVITY_SCAN_ROWS);
  const hiddenActiveScanCount = activeScans.length - visibleActiveScans.length;
  const activeScansMayBeTruncated = activeScans.length >= ACTIVE_SCAN_SNAPSHOT_LIMIT;
  const totalActiveLabel = formatBadgeCount(totalActive, activeScansMayBeTruncated);

  // Keep mounted while popover is open so Radix can animate closed
  if (hideWhenEmpty && totalActive === 0 && !open) return null;

  return (
    <PopoverPrimitive.Root key={location.pathname} open={open} onOpenChange={setOpen}>
      <PopoverPrimitive.Trigger asChild>
        <button
          type="button"
          aria-label={
            totalActive > 0
              ? t(
                  activeScansMayBeTruncated
                    ? "web.admin.common.serverActivity.trigger.atLeast"
                    : "web.admin.common.serverActivity.trigger.active",
                  {
                    count: totalActive,
                    count_text: String(totalActive),
                  },
                )
              : t("web.admin.common.serverActivity.trigger.idle")
          }
          className={cn(
            "hover:bg-accent/60 relative flex h-9 w-9 items-center justify-center rounded-xl transition-colors",
            className,
          )}
        >
          <Activity
            className={`h-[18px] w-[18px] ${
              totalActive > 0 ? "text-primary" : "text-muted-foreground"
            }`}
          />
          {totalActive > 0 && (
            <span className="absolute -end-0.5 -top-0.5 flex h-[18px] min-w-[18px] animate-[pulse-opacity_2s_ease-in-out_infinite] items-center justify-center rounded-full bg-red-500 px-1 text-[10px] font-bold text-white shadow-sm">
              {totalActiveLabel}
            </span>
          )}
          {showConnectionProblem && (
            <span
              className="ring-background absolute -end-0.5 -bottom-0.5 h-2.5 w-2.5 rounded-full bg-red-500 ring-2"
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
            <span className="text-[13px] font-bold">
              {t("web.admin.common.serverActivity.title")}
            </span>
            {connectionState !== "live" && (
              <span className="text-warning text-[10px] font-medium">
                {connectionState === "connecting"
                  ? t("web.admin.common.serverActivity.connecting")
                  : t("web.admin.common.serverActivity.disconnected")}
              </span>
            )}
          </div>

          <div className="max-h-[400px] overflow-y-auto">
            {/* Recent preparation failures are not activity, but hiding them
                behind the empty state would leave them nowhere to be seen. */}
            {totalActive === 0 && scansLoaded && !preparationCounts?.failed_recent ? (
              <div className="text-muted-foreground px-4 py-8 text-center text-sm">
                {t("web.admin.common.serverActivity.empty")}
              </div>
            ) : (
              <>
                {/* Streams */}
                <ActivitySection
                  title={t("web.admin.common.serverActivity.streams")}
                  count={sessions.length}
                  href="/admin/activity"
                  onNavigate={() => setOpen(false)}
                >
                  {sessions.length > 0 ? (
                    <div className="space-y-1.5">
                      {Object.entries(streamCounts)
                        .sort(([a], [b]) => compareActivityMethods(a, b))
                        .map(([method, count]) => (
                          <StreamCountRow key={method} method={method} count={count} />
                        ))}
                    </div>
                  ) : (
                    <EmptyRow>{t("web.admin.common.serverActivity.noStreams")}</EmptyRow>
                  )}
                </ActivitySection>

                {/* Tasks */}
                <ActivitySection
                  title={t("web.admin.common.serverActivity.tasks")}
                  count={runningTasks.length}
                  href="/admin/tasks"
                  onNavigate={() => setOpen(false)}
                >
                  {runningTasks.length > 0 ? (
                    <div className="space-y-2">
                      {runningTasks.map((task) => (
                        <TaskRow key={task.key} task={task} onNavigate={() => setOpen(false)} />
                      ))}
                    </div>
                  ) : (
                    <EmptyRow>{t("web.admin.common.serverActivity.noTasks")}</EmptyRow>
                  )}
                </ActivitySection>

                {/* Scans */}
                <ActivitySection
                  title={t("web.admin.common.serverActivity.scans")}
                  count={activeScans.length}
                  countIsLowerBound={activeScansMayBeTruncated}
                  href="/admin/libraries"
                  onNavigate={() => setOpen(false)}
                  last={!preparationCounts}
                >
                  {activeScans.length > 0 ? (
                    <div className="space-y-1.5">
                      {visibleActiveScans.map((scan) => (
                        <ScanRow
                          key={scan.id}
                          scan={scan}
                          libraryName={libraryName(scan.library_id)}
                        />
                      ))}
                      {hiddenActiveScanCount > 0 && (
                        <MoreRows
                          count={hiddenActiveScanCount}
                          countIsLowerBound={activeScansMayBeTruncated}
                        />
                      )}
                    </div>
                  ) : (
                    <EmptyRow>{t("web.admin.common.serverActivity.noScans")}</EmptyRow>
                  )}
                </ActivitySection>

                {preparationCounts && (
                  <ActivitySection
                    title={t("web.admin.common.serverActivity.preparing")}
                    count={activePreparations}
                    href={PREPARATIONS_HREF}
                    onNavigate={() => setOpen(false)}
                    last
                  >
                    <PreparationsSummary
                      running={runningPreparations}
                      queued={preparationCounts.queued}
                      retrying={preparationCounts.retrying}
                      paused={preparationCounts.paused}
                      failed={preparationCounts.failed_recent}
                      onNavigate={() => setOpen(false)}
                    />
                  </ActivitySection>
                )}
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
  countIsLowerBound = false,
  href,
  onNavigate,
  last = false,
  children,
}: {
  title: string;
  count: number;
  countIsLowerBound?: boolean;
  href: string;
  onNavigate: () => void;
  last?: boolean;
  children: React.ReactNode;
}) {
  const t = useT();
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
              {formatBadgeCount(count, countIsLowerBound)}
            </span>
          )}
        </div>
        <Link
          to={href}
          onClick={onNavigate}
          className="text-muted-foreground hover:text-primary flex items-center gap-0.5 text-[10px] font-medium transition-colors"
        >
          {t("web.admin.common.serverActivity.viewAll")}
          <ChevronRight className="h-3 w-3" />
        </Link>
      </div>
      {children}
    </div>
  );
}

function formatBadgeCount(count: number, countIsLowerBound = false) {
  if (countIsLowerBound) {
    return count > MAX_BADGE_COUNT ? `${MAX_BADGE_COUNT}+` : `${count}+`;
  }
  return count > MAX_BADGE_COUNT ? `${MAX_BADGE_COUNT}+` : count.toString();
}

function StreamCountRow({ method, count }: { method: string; count: number }) {
  const t = useT();
  const meta = activityMethodMeta(method);
  return (
    <div className="flex items-center gap-2.5">
      <span className={`h-2 w-2 rounded-full ${meta.swatchClass}`} />
      <span className="text-[12px] font-medium">
        {t("web.admin.common.serverActivity.streamCount", {
          count,
          count_text: String(count),
          method: meta.label,
        })}
      </span>
    </div>
  );
}

function TaskRow({ task, onNavigate }: { task: TaskInfo; onNavigate: () => void }) {
  const t = useT();
  const hasDeterminateProgress = task.progress > 0;

  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between">
        {/* Hidden workers are absent from the task list, so link each row to its own page. */}
        <Link
          to={`/admin/tasks/${task.key}`}
          onClick={onNavigate}
          className="hover:text-primary truncate text-[12px] font-medium transition-colors"
        >
          {task.name}
        </Link>
        {hasDeterminateProgress ? (
          <span className="text-muted-foreground ms-2 shrink-0 text-[10px] font-semibold tabular-nums">
            {formatTaskProgress(task.progress)}
          </span>
        ) : (
          <span className="text-primary ms-2 flex shrink-0 items-center gap-1 text-[10px] font-semibold">
            <Loader className="h-3 w-3 animate-spin" aria-hidden="true" />
            {t("web.admin.common.serverActivity.running")}
          </span>
        )}
      </div>
      {hasDeterminateProgress && (
        <div className="bg-muted h-1.5 overflow-hidden rounded-full">
          <div
            className="bg-primary h-full rounded-full transition-[width] duration-300 ease-out"
            style={{ width: `${clampTaskProgress(task.progress)}%` }}
          />
        </div>
      )}
    </div>
  );
}

function PreparationsSummary({
  running,
  queued,
  retrying,
  paused,
  failed,
  onNavigate,
}: {
  running: AdminDownloadPreparation[];
  queued: number;
  retrying: number;
  paused: number;
  failed: number;
  onNavigate: () => void;
}) {
  const t = useT();
  const visible = running.slice(0, MAX_ACTIVITY_PREPARATION_ROWS);
  // Running jobs past the visible rows join the waiting line, so the counts
  // still add up to the section badge.
  const hiddenRunning = running.length - visible.length;
  const waiting = [
    hiddenRunning > 0
      ? t("web.admin.common.serverActivity.moreRunning", {
          count: hiddenRunning,
          count_text: String(hiddenRunning),
        })
      : "",
    queued > 0 ? t("web.admin.common.serverActivity.queued", localeCount(queued)) : "",
    retrying > 0 ? t("web.admin.common.serverActivity.retrying", localeCount(retrying)) : "",
  ].filter(Boolean);

  if (visible.length === 0 && waiting.length === 0 && paused === 0 && failed === 0) {
    return <EmptyRow>{t("web.admin.common.serverActivity.noPreparations")}</EmptyRow>;
  }
  return (
    <div className="space-y-2.5">
      {visible.map((prep) => (
        <PreparationRow key={prep.id} prep={prep} onNavigate={onNavigate} />
      ))}
      {waiting.length > 0 && (
        <div
          className={cn(
            "text-muted-foreground flex items-center gap-1.5 text-[11px]",
            visible.length > 0 &&
              "border-t border-[color-mix(in_srgb,var(--border)_35%,transparent)] pt-1.5",
          )}
        >
          <Clock className="h-3 w-3 shrink-0" aria-hidden="true" />
          {waiting.join(" · ")}
        </div>
      )}
      {paused > 0 && (
        <div className="text-muted-foreground flex items-center gap-1.5 text-[11px]">
          <Pause className="h-3 w-3 shrink-0" aria-hidden="true" />
          {t("web.admin.common.serverActivity.paused", localeCount(paused))}
        </div>
      )}
      {failed > 0 && (
        <div className="text-destructive flex items-center gap-1.5 text-[11px] font-medium">
          <AlertTriangle className="h-3 w-3 shrink-0" aria-hidden="true" />
          {t("web.admin.common.serverActivity.failedRecent", localeCount(failed))}
        </div>
      )}
    </div>
  );
}

function PreparationRow({
  prep,
  onNavigate,
}: {
  prep: AdminDownloadPreparation;
  onNavigate: () => void;
}) {
  const t = useT();
  const percent = preparationPercent(prep);
  const worker = preparationWorker(prep);
  const detail = [
    formatPreparationKind(prep),
    worker?.name,
    formatRemaining(preparationRemainingSeconds(prep)),
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div className="space-y-1">
      <div className="flex items-center justify-between">
        <Link
          to={PREPARATIONS_HREF}
          onClick={onNavigate}
          className="hover:text-primary truncate text-[12px] font-medium transition-colors"
        >
          {preparationCompactTitle(prep)}
        </Link>
        {percent != null ? (
          <span className="text-muted-foreground ms-2 shrink-0 text-[10px] font-semibold tabular-nums">
            {t("web.admin.common.serverActivity.percent", { percent: String(Math.floor(percent)) })}
          </span>
        ) : (
          <span className="text-primary ms-2 flex shrink-0 items-center gap-1 text-[10px] font-semibold">
            <Loader className="h-3 w-3 animate-spin" aria-hidden="true" />
            {preparationRunningLabel(prep)}
          </span>
        )}
      </div>
      {percent != null && (
        <div
          className="bg-muted h-1.5 overflow-hidden rounded-full"
          role="progressbar"
          aria-label={t("web.admin.common.serverActivity.progressLabel", {
            title: preparationCompactTitle(prep),
          })}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.floor(percent)}
        >
          <div
            className="bg-primary h-full rounded-full transition-[width] duration-300 ease-out"
            style={{ width: `${percent}%` }}
          />
        </div>
      )}
      <div className="text-muted-foreground truncate text-[10px]">{detail}</div>
    </div>
  );
}

function ScanRow({ scan, libraryName }: { scan: ScanRun; libraryName: string }) {
  const t = useT();
  const progressLabel = formatScanProgress(scan, t);
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
          <span className="text-muted-foreground ms-auto shrink-0 text-[10px]">
            {scan.status === "running"
              ? t("web.admin.common.serverActivity.scanning")
              : t("web.admin.common.serverActivity.scanQueued")}
          </span>
        </div>
        <div className="text-muted-foreground truncate text-[10px]">
          {formatScanLabel(scan, t)}
          {scan.path ? DETAIL_SEPARATOR + scan.path : null}
        </div>
        {progressLabel && (
          <div className="text-muted-foreground/80 truncate text-[10px]">{progressLabel}</div>
        )}
      </div>
    </div>
  );
}

const SCAN_MODE_LABELS: Record<string, MessageKey> = {
  library: "web.admin.common.serverActivity.scanMode.library",
  subtree: "web.admin.common.serverActivity.scanMode.subtree",
  file: "web.admin.common.serverActivity.scanMode.file",
};

function formatScanLabel(scan: ScanRun, t: Translate) {
  const key = SCAN_MODE_LABELS[scan.mode];
  return key ? t(key) : scan.mode;
}

function formatScanProgress(scan: ScanRun, t: Translate) {
  const result = scan.result;
  if (!result) {
    return null;
  }
  if (result.total_files && result.files_processed) {
    const percent = Math.max(
      0,
      Math.min(100, Math.round((result.files_processed / result.total_files) * 100)),
    );
    return t("web.admin.common.serverActivity.scanProgress", {
      message: result.message ?? t("web.admin.common.serverActivity.processingFiles"),
      processed: result.files_processed.toLocaleString(formatLocale()),
      total: result.total_files.toLocaleString(formatLocale()),
      percent: String(percent),
    });
  }
  if (result.message) {
    return result.message;
  }
  return null;
}

function EmptyRow({ children }: { children: React.ReactNode }) {
  return <div className="text-muted-foreground py-1 text-[11px]">{children}</div>;
}

function MoreRows({
  count,
  countIsLowerBound = false,
}: {
  count: number;
  countIsLowerBound?: boolean;
}) {
  const t = useT();
  return (
    <div className="text-muted-foreground border-t border-[color-mix(in_srgb,var(--border)_35%,transparent)] pt-1.5 text-[11px]">
      {t(
        countIsLowerBound
          ? "web.admin.common.serverActivity.moreScansAtLeast"
          : "web.admin.common.serverActivity.moreScans",
        localeCount(count),
      )}
    </div>
  );
}

/** Plural values for a count printed in the interface locale's digits. */
function localeCount(count: number) {
  return { count, count_text: count.toLocaleString(formatLocale()) };
}
