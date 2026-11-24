import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  CheckCircle2,
  DatabaseBackup,
  Ellipsis,
  HardDrive,
  Pencil,
  Square,
  Trash2,
} from "lucide-react";

import type { LibraryMountCheckResponse, LibraryRealtimeMonitoringEntry } from "@/api/types";
import { RealtimeMonitoringBadge } from "@/components/admin/libraries/RealtimeMonitoringBadge";
import { realtimeMonitoringNeedsAttention } from "@/components/admin/libraries/realtimeMonitoring";
import type { LibraryRow } from "@/components/admin/libraries/useLibraryRows";
import { TrickplayLibraryBadge } from "@/components/admin/trickplay/TrickplayLibraryBadge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import type { AdminTrickplayLibrary } from "@/hooks/queries/admin/trickplay";
import { numberText, useT } from "@/i18n";
import { formatRelativeTime } from "@/lib/date";
import { formatDateTime } from "@/lib/datetime";
import { cn } from "@/lib/utils";

// Not translated: a separator glyph.
const SEPARATOR = "·";
const SPACED_SEPARATOR = ` ${SEPARATOR} `;

/**
 * The Status column badges for a library row or card.
 * Keeps healthy idle libraries quiet (dropping the noisy "Enabled" pill),
 * surfaces live scans/refreshes as a clickable pill into Scan Activity,
 * and shows Disabled, scan warnings, monitoring warnings, and preview status.
 */
export function LibraryStatusBadges({
  row,
  realtimeMonitoring,
  trickplay,
  onViewActivity,
  showIdle = false,
  className,
}: {
  row: LibraryRow;
  realtimeMonitoring: LibraryRealtimeMonitoringEntry | undefined;
  trickplay: AdminTrickplayLibrary | undefined;
  onViewActivity?: (libraryId: number) => void;
  /** Show a quiet muted "Idle" indicator when nothing else needs attention. */
  showIdle?: boolean;
  className?: string;
}) {
  const t = useT();
  const { library, runningScanCount, queuedScanCount, activeRefreshJob } = row;
  const hasScans = runningScanCount > 0 || queuedScanCount > 0;
  const hasMonitoringWarning = realtimeMonitoringNeedsAttention(realtimeMonitoring);
  const hasAnyPill =
    !library.enabled ||
    hasScans ||
    activeRefreshJob !== undefined ||
    row.hasScanWarning ||
    hasMonitoringWarning ||
    trickplay !== undefined;

  const scanPillParts: string[] = [];
  if (runningScanCount > 0) {
    scanPillParts.push(
      t("web.admin.libraries.scans.running", {
        count: runningScanCount,
        count_text: numberText(runningScanCount),
      }),
    );
  }
  if (queuedScanCount > 0) {
    scanPillParts.push(
      t("web.admin.libraries.scans.queued", {
        count: queuedScanCount,
        count_text: numberText(queuedScanCount),
      }),
    );
  }

  return (
    <div className={cn("flex flex-wrap items-center gap-1.5", className)}>
      {!library.enabled ? (
        <Badge variant="destructive">{t("web.admin.libraries.list.disabled")}</Badge>
      ) : null}

      {hasScans ? (
        onViewActivity ? (
          <button
            type="button"
            className={cn(
              "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[0.6875rem] font-medium tabular-nums transition-colors",
              runningScanCount > 0
                ? "border-success/35 bg-success/10 text-success hover:bg-success/20"
                : "border-border bg-secondary text-secondary-foreground hover:bg-accent",
            )}
            title={t("web.admin.libraries.list.viewScansFor", { name: library.name })}
            onClick={() => onViewActivity(library.id)}
          >
            {runningScanCount > 0 ? (
              <span className="relative flex h-1.5 w-1.5 shrink-0">
                <span className="bg-success absolute inline-flex h-full w-full animate-ping rounded-full opacity-75" />
                <span className="bg-success relative inline-flex h-1.5 w-1.5 rounded-full" />
              </span>
            ) : null}
            {scanPillParts.join(SPACED_SEPARATOR)}
            <span className="sr-only">
              {SPACED_SEPARATOR}
              {t("web.admin.libraries.list.viewScansFor", { name: library.name })}
            </span>
          </button>
        ) : (
          <Badge variant="secondary" className="tabular-nums">
            {scanPillParts.join(SPACED_SEPARATOR)}
          </Badge>
        )
      ) : null}

      {activeRefreshJob
        ? (() => {
            const refreshRunning = activeRefreshJob.status === "running";
            const refreshLabel = refreshRunning
              ? t("web.admin.libraries.activity.refreshingMetadata")
              : t("web.admin.libraries.activeWork.refreshQueued");
            return onViewActivity ? (
              <button
                type="button"
                className={cn(
                  "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[0.6875rem] font-medium transition-colors",
                  refreshRunning
                    ? "border-primary/30 bg-primary/10 text-primary hover:bg-primary/20"
                    : "border-border bg-secondary text-secondary-foreground hover:bg-accent",
                )}
                onClick={() => onViewActivity(library.id)}
              >
                <DatabaseBackup className="h-3 w-3 shrink-0" aria-hidden="true" />
                {refreshLabel}
                <span className="sr-only">
                  {SPACED_SEPARATOR}
                  {library.name}
                </span>
              </button>
            ) : (
              <Badge variant="secondary">{refreshLabel}</Badge>
            );
          })()
        : null}

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

      {showIdle && !hasAnyPill ? (
        <span className="text-muted-foreground inline-flex items-center gap-1.5 text-xs">
          <span className="bg-muted-foreground/40 h-1.5 w-1.5 rounded-full" aria-hidden="true" />
          {t("web.admin.libraries.list.idle")}
        </span>
      ) : null}
    </div>
  );
}

/** Relative last-scanned time with an accessible popover for the exact timestamp, plus any scan warning time. */
export function LibraryLastScanned({ row }: { row: LibraryRow }) {
  const t = useT();
  const { library } = row;
  const scannedAt = library.last_scanned_at;
  const exact = scannedAt ? formatDateTime(scannedAt) : null;
  const relative = scannedAt
    ? formatRelativeTime(scannedAt, { rounding: "floor", absoluteAfterDays: 30 })
    : null;
  const showExactPopover = exact !== null && relative !== null && relative !== exact;

  return (
    <div className="space-y-0.5">
      {showExactPopover ? (
        <Popover>
          <PopoverTrigger asChild>
            <button
              type="button"
              className="hover:text-foreground cursor-pointer text-start tabular-nums underline-offset-4 hover:underline focus-visible:underline"
              title={exact}
            >
              <time dateTime={scannedAt ?? undefined}>{relative}</time>
            </button>
          </PopoverTrigger>
          <PopoverContent
            align="start"
            className="w-auto min-w-0 px-2.5 py-1.5 text-xs tabular-nums"
          >
            <time dateTime={scannedAt ?? undefined}>{exact}</time>
          </PopoverContent>
        </Popover>
      ) : (
        <div className="tabular-nums">
          {exact ? (
            <time dateTime={scannedAt ?? undefined}>{exact}</time>
          ) : (
            t("web.admin.libraries.list.neverScanned")
          )}
        </div>
      )}
      {library.scan_warning_at ? (
        <div className="text-destructive text-[0.6875rem]">
          {t("web.admin.libraries.list.warningAt", {
            time: formatDateTime(library.scan_warning_at),
          })}
        </div>
      ) : null}
    </div>
  );
}

/**
 * Shared labeled overflow actions menu for both the desktop table and mobile cards.
 * Keeps secondary actions (Refresh Metadata, Verify Mounts) clearly labeled with
 * descriptions and separates destructive actions behind a divider.
 */
export function LibraryActionMenu({
  row,
  showEdit = false,
  canMoveUp,
  canMoveDown,
  onMove,
  touch = false,
}: {
  row: LibraryRow;
  showEdit?: boolean;
  canMoveUp?: boolean;
  canMoveDown?: boolean;
  onMove?: (direction: -1 | 1) => void;
  /** Size the trigger and menu items for touch on narrow screens. */
  touch?: boolean;
}) {
  const t = useT();
  const { library, refresh, mountCheck, cleanup } = row;
  const itemClass = touch ? "min-h-11" : undefined;

  return (
    // Not modal, like the shared ActionMenu, so an item can open a dialog cleanly.
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className={cn(
            "text-muted-foreground data-[state=open]:bg-accent data-[state=open]:text-foreground shrink-0",
            touch ? "-me-2 -mt-2 size-11 rounded-[10px]" : "h-7 w-7",
          )}
          aria-label={t("web.admin.libraries.list.moreActions", { name: library.name })}
        >
          <Ellipsis className="size-4" aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-64 max-w-[calc(100vw-2rem)]">
        <DropdownMenuItem
          className={cn(itemClass, "items-start", refresh.stop && "text-destructive")}
          disabled={refresh.disabled}
          onSelect={refresh.run}
        >
          {refresh.stop ? (
            <Square className="text-destructive mt-0.5 fill-current" aria-hidden="true" />
          ) : (
            <DatabaseBackup className="mt-0.5" aria-hidden="true" />
          )}
          <span className="min-w-0 flex-1">
            <span className="block font-medium">{refresh.label}</span>
            {!refresh.stop ? (
              <span className="text-muted-foreground block text-xs">
                {t("web.admin.libraries.list.refreshMetadataDescription")}
              </span>
            ) : null}
          </span>
        </DropdownMenuItem>

        <DropdownMenuItem
          className={cn(itemClass, "items-start")}
          disabled={mountCheck.pending}
          onSelect={(event) => {
            // Stay open so the inline verification result shows under the label.
            event.preventDefault();
            mountCheck.run();
          }}
        >
          <MountCheckButtonIcon
            className="mt-0.5 size-4"
            pending={mountCheck.pending}
            result={mountCheck.result}
          />
          <span className="min-w-0 flex-1">
            <span className="block font-medium">{t("web.admin.libraries.list.verifyMounts")}</span>
            <span className="text-muted-foreground block text-xs break-words">
              {mountCheck.message ?? t("web.admin.libraries.list.verifyMountsDescription")}
            </span>
          </span>
        </DropdownMenuItem>

        {showEdit ? (
          <DropdownMenuItem className={itemClass} onSelect={row.edit}>
            <Pencil aria-hidden="true" />
            {t("web.admin.libraries.list.edit", { name: library.name })}
          </DropdownMenuItem>
        ) : null}

        {onMove ? (
          <>
            <DropdownMenuItem
              className={itemClass}
              disabled={!canMoveUp}
              onSelect={(event) => {
                // Stay open so a library can move several places in a row.
                event.preventDefault();
                onMove(-1);
              }}
            >
              <ArrowUp aria-hidden="true" />
              {t("web.admin.libraries.list.moveUp")}
            </DropdownMenuItem>
            <DropdownMenuItem
              className={itemClass}
              disabled={!canMoveDown}
              onSelect={(event) => {
                event.preventDefault();
                onMove(1);
              }}
            >
              <ArrowDown aria-hidden="true" />
              {t("web.admin.libraries.list.moveDown")}
            </DropdownMenuItem>
          </>
        ) : null}

        <DropdownMenuSeparator />

        {cleanup.available ? (
          <DropdownMenuItem
            className={itemClass}
            variant="destructive"
            disabled={cleanup.pending}
            onSelect={cleanup.run}
          >
            <Trash2 aria-hidden="true" />
            {t("web.admin.libraries.warning.confirmCleanup")}
          </DropdownMenuItem>
        ) : null}

        <DropdownMenuItem className={itemClass} variant="destructive" onSelect={row.remove}>
          <Trash2 aria-hidden="true" />
          {t("web.admin.libraries.list.delete", { name: library.name })}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
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
