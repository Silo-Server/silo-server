import { useState, type ReactNode } from "react";
import { Link } from "react-router";
import { AlertTriangle, Info, RefreshCw } from "lucide-react";
import { toast } from "sonner";
import type { AdminDownloadStorageLocation } from "@/api/v2/adminDownloadStorage";
import { V2ProblemError } from "@/api/v2/request";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAppLanguage, useT, type MessageKey, type Translate } from "@/i18n";
import {
  useAdminDownloadStorage,
  useCleanUpAdminDownloadStorageLocation,
  useDeleteAdminDownloadStorageUntrackedFiles,
} from "@/hooks/queries/admin/downloadStorage";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import { StatStrip, StatTile } from "@/pages/admin-users/detail/ui";
import {
  SCRATCH_ADMISSION_PERCENT,
  budgetLabel,
  budgetSourceLabel,
  countValues,
  diskFillPercent,
  formatPercent,
  formatStorageBytes,
  locationDirectoryLabel,
  meterSegments,
  preparedBytes,
  recordsDrift,
  storageTotals,
  storageWarnings,
  type MeterSegment,
  type MeterSegmentKind,
  type StorageWarning,
} from "./downloadStoragePresentation";
import EditLocationDialog from "./EditLocationDialog";
import { formatLocale } from "@/lib/appLocale";

const SEGMENT_CLASS: Record<MeterSegmentKind, string> = {
  in_use: "bg-chart-1",
  cached: "bg-chart-2",
  // Untracked is a state to act on, so it gets the warning color and a hatch
  // that survives without color.
  untracked:
    "bg-[repeating-linear-gradient(135deg,var(--warning)_0_3px,color-mix(in_srgb,var(--warning)_45%,transparent)_3px_6px)]",
  other: "bg-muted-foreground/35",
  free: "bg-border/70",
};

const WARNING_ACTION_LABELS: Record<NonNullable<StorageWarning["action"]>, MessageKey> = {
  edit_location: "web.admin.downloads.storage.editLocation",
  review_untracked: "web.admin.downloads.storage.review",
  review_devices: "web.admin.downloads.storage.warningAction.reviewDevices",
  view_location: "web.admin.downloads.storage.warningAction.viewFiles",
};

export type StorageTabTarget = "devices" | { files: string };

export default function StorageTab({
  onNavigate,
}: {
  onNavigate: (target: StorageTabTarget) => void;
}) {
  const t = useT();
  // Warnings, labels and sizes come from plain helpers; re-render them on a language change.
  useAppLanguage();
  const storage = useAdminDownloadStorage();
  const [editing, setEditing] = useState<AdminDownloadStorageLocation | null>(null);
  const [untrackedTarget, setUntrackedTarget] = useState<AdminDownloadStorageLocation | null>(null);
  const cleanUp = useCleanUpAdminDownloadStorageLocation();
  const deleteUntracked = useDeleteAdminDownloadStorageUntrackedFiles();

  if (storage.isLoading) {
    return (
      <div
        className="space-y-4"
        role="status"
        aria-label={t("web.admin.downloads.storage.loading")}
      >
        <Skeleton className="h-24 rounded-2xl" />
        <div className="grid gap-4 lg:grid-cols-2">
          <Skeleton className="h-64 rounded-2xl" />
          <Skeleton className="h-64 rounded-2xl" />
        </div>
      </div>
    );
  }
  if (storage.isError || !storage.data) {
    return (
      <div className="surface-panel space-y-3 rounded-2xl p-6">
        <p>{t("web.admin.downloads.storage.loadError")}</p>
        <Button variant="outline" size="sm" onClick={() => void storage.refetch()}>
          {t("common.tryAgain")}
        </Button>
      </div>
    );
  }
  const data = storage.data;
  const totals = storageTotals(data);
  const warnings = storageWarnings(data);

  function runCleanUp(location: AdminDownloadStorageLocation) {
    cleanUp.mutate(location.key, {
      onSuccess: (result) =>
        toast.success(
          result.freed_bytes > 0
            ? t("web.admin.downloads.storage.cleanUp.freed", {
                size: formatStorageBytes(result.freed_bytes),
                name: location.name,
              })
            : t("web.admin.downloads.storage.cleanUp.nothing", { name: location.name }),
        ),
      onError: () =>
        toast.error(t("web.admin.downloads.storage.cleanUp.failed", { name: location.name })),
    });
  }

  function onWarningAction(warning: StorageWarning) {
    const location = data.locations.find((l) => l.key === warning.location);
    switch (warning.action) {
      case "edit_location":
        if (location) setEditing(location);
        break;
      case "review_untracked":
        if (location) setUntrackedTarget(location);
        break;
      case "review_devices":
        onNavigate("devices");
        break;
      case "view_location":
        if (location) onNavigate({ files: location.key });
        break;
    }
  }

  return (
    <div className="space-y-5">
      <StatStrip>
        <StatTile
          label={t("web.admin.downloads.storage.stats.onDisk")}
          value={formatStorageBytes(totals.onDisk)}
          detail={t("web.admin.downloads.storage.stats.onDiskDetail", {
            locations: t(
              "web.admin.downloads.storage.stats.locationCount",
              countValues(data.locations.length),
            ),
            files: t(
              "web.admin.downloads.storage.fileCount",
              countValues(totals.files, totals.files.toLocaleString(formatLocale())),
            ),
          })}
        />
        <StatTile
          label={t("web.admin.downloads.storage.stats.inUse")}
          value={formatStorageBytes(totals.inUse)}
          detail={t("web.admin.downloads.storage.stats.inUseDetail")}
        />
        <StatTile
          label={t("web.admin.downloads.storage.stats.cached")}
          value={formatStorageBytes(totals.cached)}
          detail={t(
            "web.admin.downloads.storage.stats.cachedDetail",
            countValues(data.cache_hours),
          )}
        />
        <StatTile
          label={t("web.admin.downloads.storage.stats.untracked")}
          value={
            <span className={totals.untracked > 0 ? "text-warning" : undefined}>
              {formatStorageBytes(totals.untracked)}
            </span>
          }
          detail={t("web.admin.downloads.storage.stats.untrackedDetail")}
        />
        <StatTile
          label={t("web.admin.downloads.storage.stats.freed")}
          value={formatStorageBytes(data.freed_last_30_days_bytes)}
          detail={
            data.preparing_jobs > 0 ? (
              <Link to="/admin/downloads?tab=preparation" className="underline underline-offset-3">
                {t(
                  "web.admin.downloads.storage.stats.preparingNow",
                  countValues(data.preparing_jobs),
                )}
              </Link>
            ) : (
              t("web.admin.downloads.storage.stats.nothingPreparing")
            )
          }
        />
      </StatStrip>

      {warnings.length > 0 ? (
        <div className="space-y-2">
          {warnings.map((warning) => (
            <WarningBanner
              key={warning.id}
              warning={warning}
              onAction={() => onWarningAction(warning)}
            />
          ))}
        </div>
      ) : null}

      <div className="grid gap-4 lg:grid-cols-2">
        {data.locations.map((location) => (
          <LocationCard
            key={location.key}
            location={location}
            ceiling={data.disk_ceiling_percent}
            cleaningUp={cleanUp.isPending && cleanUp.variables === location.key}
            cleanUpBusy={cleanUp.isPending}
            onCleanUp={() => runCleanUp(location)}
            onEdit={() => setEditing(location)}
            onReviewUntracked={() => setUntrackedTarget(location)}
            onBrowse={() => onNavigate({ files: location.key })}
          />
        ))}
      </div>
      <MeterKey ceiling={data.disk_ceiling_percent} />

      <EditLocationDialog location={editing} storage={data} onClose={() => setEditing(null)} />
      <ConfirmDialog
        open={untrackedTarget !== null}
        onOpenChange={(open) => {
          if (!open) setUntrackedTarget(null);
        }}
        title={
          untrackedTarget
            ? t("web.admin.downloads.storage.untracked.confirmTitle", {
                name: untrackedTarget.name,
              })
            : ""
        }
        description={
          untrackedTarget
            ? t("web.admin.downloads.storage.untracked.confirmDescription", {
                ...countValues(untrackedTarget.untracked_files),
                size: formatStorageBytes(untrackedTarget.untracked_bytes),
              })
            : ""
        }
        confirmLabel={t("web.admin.downloads.storage.untracked.confirm")}
        variant="destructive"
        irreversible
        isPending={deleteUntracked.isPending}
        onConfirm={() => {
          if (!untrackedTarget) return;
          const target = untrackedTarget;
          deleteUntracked.mutate(target.key, {
            onSuccess: (result) => {
              const deleted = t("web.admin.downloads.storage.untracked.deleted", {
                ...countValues(result.files),
                size: formatStorageBytes(result.bytes),
              });
              if (result.failed_files > 0) {
                const failed = t("web.admin.downloads.storage.untracked.failed", {
                  ...countValues(result.failed_files),
                  name: target.name,
                });
                toast.error(
                  result.files > 0
                    ? t("web.admin.downloads.storage.untracked.failedAndDeleted", {
                        failed,
                        deleted,
                      })
                    : failed,
                );
              } else {
                toast.success(
                  result.files > 0 ? deleted : t("web.admin.downloads.storage.untracked.noneLeft"),
                );
              }
              setUntrackedTarget(null);
            },
            // The dialog closes on confirm, so a failure is reported here.
            onError: (error) => toast.error(untrackedDeleteError(t, error, target.name)),
          });
        }}
      />
    </div>
  );
}

/** Why deleting untracked files failed. */
function untrackedDeleteError(t: Translate, error: Error, name: string): ReactNode {
  if (error instanceof V2ProblemError && error.status === 503) {
    return t("web.admin.downloads.storage.untracked.listFailed", {
      name,
      location: (chunks) => chunks,
    });
  }
  return t("web.admin.downloads.storage.untracked.deleteFailed");
}

function WarningBanner({ warning, onAction }: { warning: StorageWarning; onAction: () => void }) {
  const t = useT();
  const Icon = warning.tone === "warning" ? AlertTriangle : Info;
  return (
    <div
      role={warning.tone === "warning" ? "alert" : "status"}
      className={cn(
        "flex flex-col gap-3 rounded-xl border px-4 py-3 text-sm sm:flex-row sm:items-start",
        warning.tone === "warning" ? "border-warning/30 bg-warning/8" : "border-info/25 bg-info/6",
      )}
    >
      <Icon
        className={cn(
          "mt-0.5 size-4 shrink-0",
          warning.tone === "warning" ? "text-warning" : "text-info",
        )}
        aria-hidden="true"
      />
      <p className="flex-1">
        <span className="font-semibold">{warning.title}</span> {warning.body}
      </p>
      {warning.action ? (
        <Button variant="outline" size="sm" className="self-start" onClick={onAction}>
          {t(WARNING_ACTION_LABELS[warning.action])}
        </Button>
      ) : null}
    </div>
  );
}

function LocationCard({
  location,
  ceiling,
  cleaningUp,
  cleanUpBusy,
  onCleanUp,
  onEdit,
  onReviewUntracked,
  onBrowse,
}: {
  location: AdminDownloadStorageLocation;
  ceiling: number;
  /** This location's clean-up is running. */
  cleaningUp: boolean;
  /** A clean-up is running somewhere; one at a time keeps every result reported. */
  cleanUpBusy: boolean;
  onCleanUp: () => void;
  onEdit: () => void;
  onReviewUntracked: () => void;
  onBrowse: () => void;
}) {
  const t = useT();
  const usage = location.usage;
  const segments = meterSegments(location);
  const fill = diskFillPercent(location);
  const nearCeiling = fill !== null && fill >= ceiling - 5;
  const drift = recordsDrift(location);
  const indexed = preparedBytes(location);
  const offline = location.kind === "node" && !location.online;
  const measured = formatRelativeTime(usage?.measured_at);
  const files = location.in_use_files + location.cached_files;
  return (
    <section
      aria-label={location.name}
      className={cn(
        "surface-panel space-y-4 rounded-2xl p-4 sm:p-5",
        nearCeiling && "ring-warning/40 ring-1",
        offline && "opacity-80",
      )}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <h2 className="truncate text-base font-semibold">{location.name}</h2>
          <StatusPill location={location} />
        </div>
        <Button variant="ghost" size="sm" onClick={onCleanUp} disabled={cleanUpBusy || offline}>
          <RefreshCw className={cn("size-3.5", cleaningUp && "animate-spin")} aria-hidden="true" />
          {t("web.admin.downloads.storage.card.cleanUp")}
        </Button>
      </div>

      <div className="flex flex-wrap items-center gap-2 text-sm">
        <code className="text-muted-foreground font-mono text-xs break-all">
          {locationDirectoryLabel(location)}
        </code>
        {usage?.shares_scratch ? (
          <Badge variant="outline" className="border-warning/40 text-warning">
            {t("web.admin.downloads.storage.card.sharesScratch")}
          </Badge>
        ) : usage ? (
          <Badge variant="secondary">
            {usage.fs_type
              ? t("web.admin.downloads.storage.card.ownDiskWithType", { fs_type: usage.fs_type })
              : t("web.admin.downloads.storage.card.ownDisk")}
          </Badge>
        ) : null}
        {usage?.ephemeral ? (
          <Badge variant="outline" className="border-warning/40 text-warning">
            {t("web.admin.downloads.storage.card.temporary")}
          </Badge>
        ) : null}
      </div>
      {location.pending_dir ? (
        <p className="text-muted-foreground text-xs">
          {t("web.admin.downloads.storage.card.pendingDir", {
            dir: location.pending_dir,
            name: location.name,
            code: (chunks) => <code className="font-mono break-all">{chunks}</code>,
          })}
        </p>
      ) : null}

      {segments && usage ? (
        <div className="space-y-2">
          <div className="text-muted-foreground flex justify-between gap-2 text-xs">
            <span>
              {t("web.admin.downloads.storage.card.diskUsed", {
                used: formatStorageBytes(usage.fs_used_bytes),
                total: formatStorageBytes(usage.fs_total_bytes),
                percent: formatPercent(fill ?? 0),
                b: (chunks) => <span className="text-foreground font-semibold">{chunks}</span>,
              })}
            </span>
            {usage.stale || offline ? (
              <span>
                {measured
                  ? t("web.admin.downloads.storage.card.asOf", { when: measured })
                  : t("web.admin.downloads.storage.card.asOfEarlier")}
              </span>
            ) : null}
          </div>
          <DiskMeter
            segments={segments}
            ceiling={ceiling}
            scratch={usage.shares_scratch}
            dimmed={offline}
          />
          <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
            {segments
              .filter((s) => s.kind !== "free" || s.bytes > 0)
              .filter((s) => s.kind !== "untracked" || s.bytes > 0)
              .map((s) => (
                <div key={s.kind} className="flex items-center justify-between gap-2">
                  <dt className="text-muted-foreground flex items-center gap-1.5">
                    <span
                      className={cn("inline-block size-2.5 rounded-[3px]", SEGMENT_CLASS[s.kind])}
                      aria-hidden="true"
                    />
                    {s.label}
                  </dt>
                  <dd className="tabular-nums">{formatStorageBytes(s.bytes)}</dd>
                </div>
              ))}
          </dl>
        </div>
      ) : (
        <p className="text-muted-foreground text-sm">
          {offline
            ? t("web.admin.downloads.storage.card.notMeasuredOffline")
            : t("web.admin.downloads.storage.card.waitingMeasurement")}
        </p>
      )}

      <div className="border-border/60 space-y-1.5 border-t pt-3 text-xs">
        <div className="flex justify-between gap-2">
          <span>
            {location.budget_bytes > 0
              ? t("web.admin.downloads.storage.card.preparedWithBudget", {
                  size: formatStorageBytes(indexed),
                  budget: formatStorageBytes(location.budget_bytes),
                  b: (chunks) => <span className="font-semibold">{chunks}</span>,
                })
              : t("web.admin.downloads.storage.card.prepared", {
                  size: formatStorageBytes(indexed),
                  b: (chunks) => <span className="font-semibold">{chunks}</span>,
                })}
          </span>
          <span className="text-muted-foreground">
            {location.budget_bytes > 0 ? budgetSourceLabel(location) : budgetLabel(location)}
          </span>
        </div>
        {location.budget_bytes > 0 ? (
          <div className="bg-border/70 h-1 overflow-hidden rounded-full" aria-hidden="true">
            <div
              className="bg-foreground/60 h-full rounded-full"
              style={{ width: `${Math.min(100, (indexed / location.budget_bytes) * 100)}%` }}
            />
          </div>
        ) : null}
        {location.untracked_bytes > 0 ? (
          <p className="text-warning">
            {t("web.admin.downloads.storage.card.recordsUntracked", {
              indexed: formatStorageBytes(indexed),
              untracked: formatStorageBytes(location.untracked_bytes),
              review: (chunks) => (
                <button
                  type="button"
                  className="underline underline-offset-3"
                  onClick={onReviewUntracked}
                >
                  {chunks}
                </button>
              ),
            })}
          </p>
        ) : drift !== 0 ? (
          <p className="text-warning">
            {t("web.admin.downloads.storage.card.recordsDrift", {
              indexed: formatStorageBytes(indexed),
              bytes: formatStorageBytes(usage?.bytes),
            })}
          </p>
        ) : usage ? (
          <p className="text-muted-foreground">
            {measured
              ? t("web.admin.downloads.storage.card.recordsMatchMeasured", { when: measured })
              : t("web.admin.downloads.storage.card.recordsMatch")}
          </p>
        ) : null}
        {location.storage_full ? (
          <p className="text-warning">
            {location.kind === "server"
              ? t("web.admin.downloads.storage.card.fullServer")
              : t("web.admin.downloads.storage.card.fullNode")}
          </p>
        ) : null}
      </div>

      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-muted-foreground text-xs">
          {[
            t(
              "web.admin.downloads.storage.fileCount",
              countValues(files, files.toLocaleString(formatLocale())),
            ),
            location.waiting_downloads > 0 &&
              t(
                "web.admin.downloads.storage.card.waiting",
                countValues(location.waiting_downloads),
              ),
            location.cleanup_backlog > 0 &&
              t("web.admin.downloads.storage.card.queued", countValues(location.cleanup_backlog)),
          ]
            .filter(Boolean)
            .join(" · ")}
        </span>
        <div className="flex gap-2">
          <Button variant="ghost" size="sm" onClick={onBrowse}>
            {t("web.admin.downloads.storage.card.browse")}
          </Button>
          <Button variant="outline" size="sm" onClick={onEdit}>
            {t("web.admin.downloads.storage.editLocation")}
          </Button>
        </div>
      </div>
    </section>
  );
}

function StatusPill({ location }: { location: AdminDownloadStorageLocation }) {
  const t = useT();
  if (location.kind === "server" || location.online) {
    return (
      <span className="text-success bg-success/10 inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium">
        <span className="size-1.5 rounded-full bg-current" aria-hidden="true" />
        {t("web.admin.downloads.storage.status.online")}
      </span>
    );
  }
  // last_health_check is the last attempt, successful or not, so it says
  // nothing about when the node was last reachable.
  return (
    <span className="text-muted-foreground bg-muted inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium">
      <span className="size-1.5 rounded-full bg-current" aria-hidden="true" />
      {location.enabled
        ? t("web.admin.downloads.storage.status.offline")
        : t("web.admin.downloads.storage.status.disabled")}
    </span>
  );
}

function DiskMeter({
  segments,
  ceiling,
  scratch,
  dimmed,
}: {
  segments: MeterSegment[];
  ceiling: number;
  scratch: boolean;
  dimmed: boolean;
}) {
  const t = useT();
  const visible = segments.filter((s) => s.bytes > 0);
  const label = visible
    .map((s) =>
      t("web.admin.downloads.storage.meter.segment", {
        label: s.label,
        size: formatStorageBytes(s.bytes),
      }),
    )
    .join(", ");
  return (
    <div
      className="relative"
      role="img"
      aria-label={t("web.admin.downloads.storage.meter.label", { segments: label })}
    >
      <div className={cn("flex h-2.5 gap-0.5", dimmed && "saturate-50")}>
        {visible.map((s, index) => (
          <span
            key={s.kind}
            className={cn(
              "h-full min-w-[2px]",
              SEGMENT_CLASS[s.kind],
              index === 0 && "rounded-l-[4px]",
              index === visible.length - 1 && "rounded-r-[4px]",
            )}
            style={{ flex: `${Math.max(s.fraction, 0.002)} 1 0%` }}
          />
        ))}
      </div>
      <span
        className="bg-warning absolute -top-1 -bottom-1 w-0.5 rounded-full"
        style={{ left: `${ceiling}%` }}
        aria-hidden="true"
      />
      {scratch ? (
        <span
          className="bg-destructive absolute -top-1 -bottom-1 w-0.5 rounded-full"
          style={{ left: `${SCRATCH_ADMISSION_PERCENT}%` }}
          aria-hidden="true"
        />
      ) : null}
    </div>
  );
}

function MeterKey({ ceiling }: { ceiling: number }) {
  const t = useT();
  return (
    <div className="text-muted-foreground flex flex-wrap gap-x-4 gap-y-1 text-xs">
      <span className="flex items-center gap-1.5">
        <span className="bg-warning inline-block h-2.5 w-0.5 rounded-full" aria-hidden="true" />
        {t("web.admin.downloads.storage.meter.ceiling", { ceiling: String(ceiling) })}
      </span>
      <span className="flex items-center gap-1.5">
        <span className="bg-destructive inline-block h-2.5 w-0.5 rounded-full" aria-hidden="true" />
        {t("web.admin.downloads.storage.meter.scratch", {
          percent: String(SCRATCH_ADMISSION_PERCENT),
        })}
      </span>
    </div>
  );
}
