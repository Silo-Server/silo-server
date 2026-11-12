import {
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  DatabaseBackup,
  HardDrive,
  RefreshCw,
  Square,
  Trash2,
} from "lucide-react";

import type {
  LibraryMountCheckResponse,
  LibraryRealtimeMonitoringEntry,
  ScanRun,
} from "@/api/types";
import { RealtimeMonitoringBadge } from "@/components/admin/libraries/RealtimeMonitoringBadge";
import { useCollapsedScans } from "@/components/admin/libraries/useCollapsedScans";
import type { LibraryRow } from "@/components/admin/libraries/useLibraryRows";
import { TrickplayLibraryBadge } from "@/components/admin/trickplay/TrickplayLibraryBadge";
import { formatJobProgress } from "@/components/adminCatalogMaintenanceFormatters";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import type { AdminTrickplayLibrary } from "@/hooks/queries/admin/trickplay";
import { useT } from "@/i18n";
import { formatDateTime } from "@/lib/datetime";
import {
  formatActiveScanMode,
  formatActiveScanProgress,
  formatActiveScanTarget,
} from "@/lib/scanRuns";
import { cn } from "@/lib/utils";

// Not translated: a separator glyph.
const SEPARATOR = "·";
const SPACED_SEPARATOR = ` ${SEPARATOR} `;

// Grows a small control's hit area to 44px without changing how it looks.
const TOUCH_HIT_AREA = "relative after:absolute after:-inset-3 after:content-['']";

/** The Enabled/Disabled, scan and warning badges of the libraries list's status column. */
export function LibraryStatusBadges({
  row,
  realtimeMonitoring,
  trickplay,
  className,
}: {
  row: LibraryRow;
  realtimeMonitoring: LibraryRealtimeMonitoringEntry | undefined;
  trickplay: AdminTrickplayLibrary | undefined;
  className?: string;
}) {
  const t = useT();
  const { library, runningScanCount, queuedScanCount } = row;
  return (
    <div className={cn("flex flex-col gap-1", className)}>
      <Badge variant={library.enabled ? "outline" : "destructive"}>
        {library.enabled
          ? t("web.admin.libraries.list.enabled")
          : t("web.admin.libraries.list.disabled")}
      </Badge>
      {runningScanCount > 0 ? (
        <Badge variant="secondary">
          {t("web.admin.libraries.scans.running", {
            count: runningScanCount,
            count_text: runningScanCount,
          })}
        </Badge>
      ) : null}
      {queuedScanCount > 0 ? (
        <Badge variant="secondary">
          {t("web.admin.libraries.scans.queued", {
            count: queuedScanCount,
            count_text: queuedScanCount,
          })}
        </Badge>
      ) : null}
      {library.scan_warning_code === "empty_root" ? (
        <Badge variant="destructive">{t("web.admin.libraries.list.emptyRootGuarded")}</Badge>
      ) : null}
      {library.scan_warning_code === "dead_root" ? (
        <Badge variant="destructive">{t("web.admin.libraries.list.rootUnreachable")}</Badge>
      ) : null}
      {library.scan_warning_code === "partial_walk" ? (
        <Badge variant="destructive">{t("web.admin.libraries.list.partialScan")}</Badge>
      ) : null}
      <RealtimeMonitoringBadge entry={realtimeMonitoring} />
      <TrickplayLibraryBadge library={trickplay} />
    </div>
  );
}

/** When the library was last scanned, and when its scan warning was raised. */
export function LibraryLastScanned({ row }: { row: LibraryRow }) {
  const t = useT();
  const { library } = row;
  return (
    <div className="space-y-1">
      <div>
        {library.last_scanned_at
          ? formatDateTime(library.last_scanned_at)
          : t("web.admin.libraries.list.neverScanned")}
      </div>
      {library.scan_warning_at ? (
        <div className="text-destructive text-[11px]">
          {t("web.admin.libraries.list.warningAt", {
            time: formatDateTime(library.scan_warning_at),
          })}
        </div>
      ) : null}
    </div>
  );
}

/**
 * A library's scan warning: what went wrong, the hint or server message, and
 * for missing or empty roots the mount check and cleanup actions. Renders
 * nothing for a library without a warning.
 */
export function LibraryScanWarning({
  row,
  touch = false,
}: {
  row: LibraryRow;
  /** Size the actions for touch. */
  touch?: boolean;
}) {
  const t = useT();
  const { library, mountCheck, cleanup } = row;
  const code = library.scan_warning_code;
  if (code === "partial_walk") {
    return (
      <div className="flex flex-col gap-2 py-1">
        <div className="text-destructive font-medium">
          {t("web.admin.libraries.list.partialScan")}
        </div>
        <div className="text-muted-foreground">
          {library.scan_warning_message ?? t("web.admin.libraries.warning.partialScanHint")}
        </div>
      </div>
    );
  }
  if (code !== "empty_root" && code !== "dead_root") return null;
  const actionClass = touch ? "max-lg:h-11" : undefined;
  return (
    <div className="flex flex-col gap-2 py-1">
      <div className="text-destructive font-medium">
        {code === "dead_root"
          ? t("web.admin.libraries.warning.deadRoot")
          : t("web.admin.libraries.warning.emptyRoot")}
      </div>
      <div className="text-muted-foreground">
        {library.scan_warning_message ??
          (code === "dead_root"
            ? t("web.admin.libraries.warning.deadRootHint")
            : t("web.admin.libraries.warning.emptyRootHint"))}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button
          variant="outline"
          size="sm"
          className={actionClass}
          title={mountCheck.message ?? t("web.admin.libraries.warning.checkMountTitle")}
          disabled={mountCheck.pending}
          onClick={mountCheck.run}
        >
          <MountCheckButtonIcon
            className="me-1 h-3.5 w-3.5"
            pending={mountCheck.pending}
            result={mountCheck.result}
          />
          {t("web.admin.libraries.warning.checkMount")}
        </Button>
        {code === "dead_root" ? (
          <Button
            variant="outline"
            size="sm"
            className={actionClass}
            title={t("web.admin.libraries.list.confirmCleanupTitle")}
            disabled={cleanup.pending}
            onClick={cleanup.run}
          >
            <Trash2 className="me-1 h-3.5 w-3.5" />
            {t("web.admin.libraries.warning.confirmCleanup")}
          </Button>
        ) : null}
      </div>
    </div>
  );
}

/** The refresh job and scans running for a library, each with its stop control. */
export function LibraryActiveWork({
  row,
  touch = false,
  className,
}: {
  row: LibraryRow;
  /** Give the small controls a 44px hit area. */
  touch?: boolean;
  className?: string;
}) {
  const t = useT();
  const { activeRefreshJob, activeScans } = row;
  return (
    <div className={cn("text-muted-foreground flex flex-col gap-1.5 text-[11px]", className)}>
      {activeRefreshJob ? (
        <div className="flex min-w-0 items-start gap-1.5">
          <StopTaskButton
            disabled={row.cancellingJobID === activeRefreshJob.id}
            label={t("web.admin.libraries.activeWork.cancelRefresh")}
            touch={touch}
            onClick={() => row.cancelJob(activeRefreshJob.id)}
          />
          <DatabaseBackup className="mt-0.5 h-3 w-3 shrink-0" />
          <div className="min-w-0 flex-1 space-y-0.5">
            <div className="flex min-w-0 items-center gap-1.5">
              <span className="text-foreground/80 font-medium">
                {t("web.admin.libraries.activeWork.metadata")}
              </span>
              <span className="truncate">
                {activeRefreshJob.message || t("web.admin.libraries.activeWork.refreshQueued")}
              </span>
            </div>
            <div className="text-muted-foreground/80 truncate text-[10px]">
              {formatJobProgress(activeRefreshJob)}
            </div>
          </div>
        </div>
      ) : null}
      {activeScans.length > 0 ? (
        <LibraryScanTasks
          scans={activeScans}
          cancelling={row.cancellingScans}
          touch={touch}
          onCancelScans={row.cancelScans}
        />
      ) : null}
    </div>
  );
}

function LibraryScanTasks({
  scans,
  cancelling,
  touch,
  onCancelScans,
}: {
  scans: ScanRun[];
  cancelling: boolean;
  touch: boolean;
  onCancelScans: () => void;
}) {
  const t = useT();
  const { expanded, setExpanded, visibleScans, collapsible } = useCollapsedScans(scans);
  const runningCount = scans.filter((scan) => scan.status === "running").length;
  const queuedCount = scans.length - runningCount;

  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className={cn("flex min-w-0 items-center gap-1.5", touch && "flex-wrap")}>
        <StopTaskButton
          disabled={cancelling}
          label={t("web.admin.libraries.activeWork.cancelScans")}
          touch={touch}
          onClick={onCancelScans}
        />
        <RefreshCw
          className={cn("h-3 w-3 shrink-0", runningCount > 0 && "animate-spin")}
          style={{ animationDuration: "2s" }}
        />
        <span className="text-foreground/80 font-medium">
          {t("web.admin.libraries.activeWork.scan")}
        </span>
        <span className="tabular-nums">
          {runningCount > 0
            ? t("web.admin.libraries.scans.running", {
                count: runningCount,
                count_text: runningCount,
              })
            : null}
          {runningCount > 0 && queuedCount > 0 ? SPACED_SEPARATOR : null}
          {queuedCount > 0
            ? t("web.admin.libraries.scans.queued", {
                count: queuedCount,
                count_text: queuedCount,
              })
            : null}
        </span>
        {collapsible ? (
          <button
            type="button"
            className={cn(
              "text-muted-foreground hover:text-foreground flex items-center gap-0.5 rounded px-1 text-[10px] transition-colors",
              touch && TOUCH_HIT_AREA,
            )}
            aria-expanded={expanded}
            onClick={() => setExpanded((current) => !current)}
          >
            {expanded ? (
              <>
                {t("web.admin.libraries.scanQueue.showLess")} <ChevronUp className="h-3 w-3" />
              </>
            ) : (
              <>
                {t("web.admin.libraries.activeWork.showAll", {
                  count: scans.length,
                  count_text: scans.length,
                })}{" "}
                <ChevronDown className="h-3 w-3" />
              </>
            )}
          </button>
        ) : null}
      </div>
      <div
        className={cn(
          "flex min-w-0 flex-col gap-0.5",
          touch ? "ps-[2.375rem]" : "ps-[3.375rem]",
          expanded && "max-h-56 overflow-y-auto",
        )}
      >
        {visibleScans.map((scan) => (
          <CompactScanRow key={scan.id} scan={scan} />
        ))}
      </div>
    </div>
  );
}

function CompactScanRow({ scan }: { scan: ScanRun }) {
  const progress = scan.status === "running" ? formatActiveScanProgress(scan) : "";

  return (
    <div className="flex min-w-0 items-center gap-1.5" title={scan.path}>
      <span
        className={cn(
          "h-1.5 w-1.5 shrink-0 rounded-full",
          scan.status === "running" ? "bg-success" : "bg-muted-foreground/30",
        )}
      />
      {scan.mode !== "file" ? (
        <span className="shrink-0 text-[10px]">{formatActiveScanMode(scan)}</span>
      ) : null}
      {scan.path ? (
        <code className="text-muted-foreground/80 truncate font-mono text-[10px]">
          {formatActiveScanTarget(scan)}
        </code>
      ) : (
        <span className="text-muted-foreground/80 truncate text-[10px]">
          {formatActiveScanTarget(scan)}
        </span>
      )}
      {progress ? (
        <span className="text-muted-foreground/60 truncate text-[10px]">
          {SEPARATOR} {progress}
        </span>
      ) : null}
    </div>
  );
}

function StopTaskButton({
  disabled,
  label,
  touch,
  onClick,
}: {
  disabled?: boolean;
  label: string;
  touch: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      className={cn(
        "text-destructive hover:bg-destructive/10 hover:text-destructive focus-visible:ring-ring flex h-5 w-5 shrink-0 items-center justify-center rounded transition-colors focus-visible:ring-2 focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50",
        touch && TOUCH_HIT_AREA,
      )}
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
    >
      <Square className="h-3 w-3 fill-current" />
    </button>
  );
}

export function MountCheckButtonIcon({
  result,
  pending,
  className = "h-3 w-3",
}: {
  result?: LibraryMountCheckResponse;
  pending: boolean;
  className?: string;
}) {
  if (pending) {
    return <HardDrive className={cn(className, "animate-pulse")} />;
  }
  if (result?.healthy) {
    return <CheckCircle2 className={cn(className, "text-success")} />;
  }
  if (result) {
    return <AlertTriangle className={cn(className, "text-destructive")} />;
  }
  return <HardDrive className={className} />;
}
