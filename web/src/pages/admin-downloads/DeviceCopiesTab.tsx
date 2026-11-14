import { Fragment, useId, useMemo, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { toast } from "sonner";
import type { AdminDownloadDevice, AdminDownloadEntry } from "@/api/v2/adminDownloadStorage";
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  useAdminDownloadDeviceEntries,
  useAdminDownloadDevices,
  useAdminDownloadStorage,
  useRevokeAdminDownloads,
} from "@/hooks/queries/admin/downloadStorage";
import { useT, type Translate } from "@/i18n";
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import { CHECKBOX, LoadMoreButton, SearchField } from "./controls";
import { countValues, formatStorageBytes } from "./downloadStoragePresentation";

type Sort = "last_seen" | "size";

/** What a revoke dialog is about: a whole device, or some of its downloads. */
type RevokeIntent =
  | { kind: "device"; device: AdminDownloadDevice }
  | { kind: "entries"; device: AdminDownloadDevice; entries: AdminDownloadEntry[] };

function deviceKey(device: AdminDownloadDevice) {
  return `${device.user_id}/${device.profile_id}/${device.device_id}`;
}

export default function DeviceCopiesTab({ initialStale = false }: { initialStale?: boolean }) {
  const t = useT();
  const storage = useAdminDownloadStorage();
  const staleDays = storage.data?.stale_device_days ?? 14;
  const [query, setQuery] = useState("");
  const [stale, setStale] = useState(initialStale);
  const [sort, setSort] = useState<Sort>("last_seen");
  const filters = useMemo(
    () => ({ q: query || undefined, stale: stale || undefined, sort }),
    [query, stale, sort],
  );
  const devices = useAdminDownloadDevices(filters);
  const rows = useMemo(
    () => devices.data?.pages.flatMap((page) => page.items) ?? [],
    [devices.data],
  );
  const [expanded, setExpanded] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<RevokeIntent | null>(null);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <SearchField
          label={t("web.admin.downloads.deviceCopies.search")}
          className="w-56"
          onSearch={setQuery}
        />
        <Button
          variant="outline"
          size="sm"
          aria-pressed={stale}
          className={cn(stale && "border-warning/50 bg-warning/10 text-warning hover:text-warning")}
          onClick={() => setStale((value) => !value)}
        >
          {t("web.admin.downloads.deviceCopies.staleFilter", {
            days: staleDays,
            days_text: staleDays,
          })}
        </Button>
        <Select value={sort} onValueChange={(value) => setSort(value as Sort)}>
          <SelectTrigger
            size="sm"
            className="w-52"
            aria-label={t("web.admin.downloads.deviceCopies.sort")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="last_seen">
              {t("web.admin.downloads.deviceCopies.sortLastSeen")}
            </SelectItem>
            <SelectItem value="size">{t("web.admin.downloads.deviceCopies.sortSize")}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div className="surface-panel overflow-hidden rounded-2xl">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8" />
              <TableHead>{t("web.admin.downloads.deviceCopies.column.device")}</TableHead>
              <TableHead>{t("web.admin.downloads.deviceCopies.column.person")}</TableHead>
              <TableHead>{t("web.admin.downloads.deviceCopies.column.lastSeen")}</TableHead>
              <TableHead className="hidden sm:table-cell">
                {t("web.admin.downloads.deviceCopies.column.copies")}
              </TableHead>
              <TableHead className="text-end">
                {t("web.admin.downloads.deviceCopies.column.onDevice")}
              </TableHead>
              <TableHead className="w-0" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {devices.isLoading ? (
              Array.from({ length: 3 }, (_, i) => (
                <TableRow key={i}>
                  <TableCell colSpan={7}>
                    <Skeleton className="h-8" />
                  </TableCell>
                </TableRow>
              ))
            ) : rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={7} className="text-muted-foreground py-10 text-center">
                  {devices.isError
                    ? t("web.admin.downloads.deviceCopies.loadError")
                    : stale
                      ? t("web.admin.downloads.deviceCopies.emptyStale")
                      : t("web.admin.downloads.deviceCopies.empty")}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((device) => {
                const key = deviceKey(device);
                const open = expanded === key;
                return (
                  <Fragment key={key}>
                    <TableRow className="bg-card/40">
                      <TableCell>
                        <button
                          type="button"
                          aria-expanded={open}
                          aria-label={
                            open
                              ? t("web.admin.downloads.deviceCopies.hideEntries", {
                                  device: device.device_name,
                                })
                              : t("web.admin.downloads.deviceCopies.showEntries", {
                                  device: device.device_name,
                                })
                          }
                          className="text-muted-foreground hover:text-foreground"
                          onClick={() => setExpanded(open ? null : key)}
                        >
                          {open ? (
                            <ChevronDown className="size-4" />
                          ) : (
                            <ChevronRight className="size-4" />
                          )}
                        </button>
                      </TableCell>
                      <TableCell>
                        <div className="font-medium">{device.device_name || device.device_id}</div>
                        <div className="text-muted-foreground text-xs">
                          {device.platform || t("web.admin.downloads.deviceCopies.unknownPlatform")}
                        </div>
                      </TableCell>
                      <TableCell>
                        <div>{device.profile_name || device.profile_id}</div>
                        <div className="text-muted-foreground text-xs">{device.username}</div>
                      </TableCell>
                      <TableCell
                        className={cn("whitespace-nowrap", device.stale && "text-warning")}
                      >
                        {formatRelativeTime(device.last_seen_at) ??
                          t("web.admin.downloads.deviceCopies.neverSeen")}
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        <div>
                          {t("web.admin.downloads.deviceCopies.copiesSummary", {
                            copies: device.copies,
                            summary:
                              [
                                device.finished &&
                                  t(
                                    "web.admin.downloads.deviceCopies.finished",
                                    countValues(device.finished),
                                  ),
                                device.waiting &&
                                  t(
                                    "web.admin.downloads.deviceCopies.waiting",
                                    countValues(device.waiting),
                                  ),
                                device.failed &&
                                  t(
                                    "web.admin.downloads.deviceCopies.failed",
                                    countValues(device.failed),
                                  ),
                              ]
                                .filter(Boolean)
                                .join(", ") || t("web.admin.downloads.deviceCopies.noneActive"),
                            muted: (chunks) => (
                              <span className="text-muted-foreground">{chunks}</span>
                            ),
                          })}
                        </div>
                        {device.revoked > 0 ? (
                          <div className="text-warning text-xs">
                            {t(
                              "web.admin.downloads.deviceCopies.revokedPending",
                              countValues(device.revoked),
                            )}
                          </div>
                        ) : device.monitors > 0 ? (
                          <div className="text-muted-foreground text-xs">
                            {t(
                              "web.admin.downloads.deviceCopies.monitors",
                              countValues(device.monitors),
                            )}
                          </div>
                        ) : null}
                      </TableCell>
                      <TableCell className="text-end whitespace-nowrap tabular-nums">
                        {formatStorageBytes(device.bytes_on_device)}
                      </TableCell>
                      <TableCell className="text-end">
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={device.copies === 0}
                          aria-label={t("web.admin.downloads.deviceCopies.revokeAllLabel", {
                            device: device.device_name || device.device_id,
                          })}
                          onClick={() => setRevoking({ kind: "device", device })}
                        >
                          {t("web.admin.downloads.deviceCopies.revokeAll")}
                        </Button>
                      </TableCell>
                    </TableRow>
                    {open ? (
                      <TableRow>
                        <TableCell colSpan={7} className="p-0">
                          <DeviceEntries
                            device={device}
                            onRevoke={(entries) =>
                              setRevoking({ kind: "entries", device, entries })
                            }
                          />
                        </TableCell>
                      </TableRow>
                    ) : null}
                  </Fragment>
                );
              })
            )}
          </TableBody>
        </Table>
      </div>
      <LoadMoreButton query={devices} />

      <RevokeDialog
        intent={revoking}
        cacheHours={storage.data?.cache_hours}
        onClose={() => setRevoking(null)}
      />
    </div>
  );
}

function entryTitle(t: Translate, entry: AdminDownloadEntry) {
  if (entry.episode) {
    return t("web.admin.downloads.deviceCopies.episodeTitle", {
      series: entry.title || t("web.admin.downloads.deviceCopies.unknownSeries"),
      season: entry.episode.season_number,
      episode: entry.episode.episode_number,
    });
  }
  return entry.title || t("web.admin.downloads.deviceCopies.unknownTitle");
}

/** "Original", or a bitrate preset such as "10mbps" as "10 Mbps". */
function qualityLabel(t: Translate, quality: string) {
  if (quality === "original") return t("web.admin.downloads.deviceCopies.qualityOriginal");
  const mbps = /^(\d+)mbps$/.exec(quality);
  return mbps ? t("web.admin.downloads.deviceCopies.qualityBitrate", { mbps: mbps[1] }) : quality;
}

function entryStatus(t: Translate, entry: AdminDownloadEntry) {
  switch (entry.status) {
    case "completed":
      return {
        label: t("web.admin.downloads.deviceCopies.status.finished"),
        className: "bg-muted text-foreground",
      };
    case "queued":
    case "preparing":
    case "ready":
      return {
        label: t("web.admin.downloads.deviceCopies.status.waiting"),
        className: "text-foreground bg-chart-1/30",
      };
    case "downloading":
      return {
        label: t("web.admin.downloads.deviceCopies.status.downloading"),
        className: "text-foreground bg-chart-1/30",
      };
    case "revoked":
      return {
        label: t("web.admin.downloads.deviceCopies.status.revoked"),
        className: "text-warning bg-warning/10",
      };
    case "failed":
      return {
        label: t("web.admin.downloads.deviceCopies.status.failed"),
        className: "text-destructive bg-destructive/10",
      };
    case "cancelled":
      return {
        label: t("web.admin.downloads.deviceCopies.status.cancelled"),
        className: "bg-muted text-muted-foreground",
      };
    default:
      return { label: entry.status, className: "bg-muted text-muted-foreground" };
  }
}

function DeviceEntries({
  device,
  onRevoke,
}: {
  device: AdminDownloadDevice;
  onRevoke: (entries: AdminDownloadEntry[]) => void;
}) {
  const t = useT();
  const entries = useAdminDownloadDeviceEntries({
    userId: device.user_id,
    profileId: device.profile_id,
    deviceId: device.device_id,
  });
  const rows = entries.data?.pages.flatMap((page) => page.items) ?? [];
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const revocable = rows.filter((row) => row.status !== "revoked");
  const chosen = revocable.filter((row) => selected.has(row.id));
  if (entries.isLoading) return <Skeleton className="m-3 h-16" />;
  return (
    <div className="space-y-2 px-3 py-3 sm:ps-12">
      {chosen.length > 0 ? (
        <div className="flex items-center gap-2 text-xs">
          <span>{t("web.admin.downloads.deviceCopies.selected", countValues(chosen.length))}</span>
          <Button
            size="sm"
            variant="outline"
            className="h-7 text-xs"
            onClick={() => onRevoke(chosen)}
          >
            {t("web.admin.downloads.deviceCopies.revokeSelected", countValues(chosen.length))}
          </Button>
        </div>
      ) : null}
      <ul className="divide-border/60 divide-y text-sm">
        {rows.map((row) => {
          const status = entryStatus(t, row);
          const title = entryTitle(t, row);
          return (
            <li key={row.id} className="flex flex-wrap items-center gap-3 py-2">
              <input
                type="checkbox"
                className={CHECKBOX}
                aria-label={t("web.admin.downloads.deviceCopies.selectEntry", { title })}
                disabled={row.status === "revoked"}
                checked={row.status !== "revoked" && selected.has(row.id)}
                onChange={() =>
                  setSelected((current) => {
                    const next = new Set(current);
                    if (next.has(row.id)) next.delete(row.id);
                    else next.add(row.id);
                    return next;
                  })
                }
              />
              <div className="min-w-0 flex-1">
                <div className="truncate font-medium">{title}</div>
                <div className="text-muted-foreground text-xs">
                  {[
                    row.episode?.title,
                    qualityLabel(t, row.effective_quality),
                    row.location_name &&
                      t("web.admin.downloads.deviceCopies.preparedOn", {
                        location: row.location_name,
                      }),
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </div>
              </div>
              <span
                className={cn("rounded-full px-2 py-0.5 text-xs font-medium", status.className)}
              >
                {status.label}
              </span>
              <span className="w-20 text-end tabular-nums">
                {formatStorageBytes(row.file_size)}
              </span>
              {row.status !== "revoked" ? (
                <button
                  type="button"
                  className="text-xs underline underline-offset-3"
                  aria-label={t("web.admin.downloads.deviceCopies.revokeEntry", { title })}
                  onClick={() => onRevoke([row])}
                >
                  {t("web.admin.downloads.deviceCopies.revoke")}
                </button>
              ) : (
                <span className="text-muted-foreground w-12 text-xs">
                  {t("web.admin.downloads.deviceCopies.sent")}
                </span>
              )}
            </li>
          );
        })}
      </ul>
      {entries.hasNextPage ? (
        <Button
          variant="ghost"
          size="sm"
          onClick={() => void entries.fetchNextPage()}
          disabled={entries.isFetchingNextPage}
        >
          {entries.isFetchingNextPage
            ? t("web.admin.downloads.deviceCopies.loadingMore")
            : t("web.admin.downloads.deviceCopies.showMore")}
        </Button>
      ) : null}
    </div>
  );
}

/** How many downloads a revoke covers and the bytes it frees on the device. */
function revokeScope(intent: RevokeIntent | null): { count: number; bytes: number } {
  if (!intent) return { count: 0, bytes: 0 };
  if (intent.kind === "device") {
    return { count: intent.device.copies, bytes: intent.device.bytes_on_device };
  }
  // Only finished copies count, as in the device row's bytes_on_device.
  const bytes = intent.entries.reduce(
    (sum, e) => sum + (e.status === "completed" ? e.file_size : 0),
    0,
  );
  return { count: intent.entries.length, bytes };
}

function RevokeDialog({
  intent,
  cacheHours,
  onClose,
}: {
  intent: RevokeIntent | null;
  /** Undefined until the storage overview has loaded. */
  cacheHours: number | undefined;
  onClose: () => void;
}) {
  const t = useT();
  const revoke = useRevokeAdminDownloads();
  const [pauseMonitors, setPauseMonitors] = useState(true);
  const [reason, setReason] = useState("");
  const pauseId = useId();
  const reasonId = useId();
  // The last intent stays on screen while the dialog animates closed.
  const [shown, setShown] = useState(intent);
  if (intent !== null && intent !== shown) {
    setShown(intent);
    setPauseMonitors(true);
    setReason("");
  }
  const device = shown?.device;
  const { count, bytes } = revokeScope(shown);
  const seen = device ? formatRelativeTime(device.last_seen_at) : null;
  const only =
    shown?.kind === "entries" && shown.entries.length === 1 ? shown.entries[0] : undefined;
  const titleDevice =
    device?.device_name || t("web.admin.downloads.deviceCopies.revokeDialog.thisDevice");
  const textDevice =
    device?.device_name || t("web.admin.downloads.deviceCopies.revokeDialog.deviceFallback");
  const description = device?.profile_name
    ? t("web.admin.downloads.deviceCopies.revokeDialog.descriptionProfile", {
        count,
        profile: device.profile_name,
        device: textDevice,
      })
    : t("web.admin.downloads.deviceCopies.revokeDialog.description", {
        count,
        device: textDevice,
      });

  function confirm() {
    if (!shown) return;
    const target =
      shown.kind === "device"
        ? {
            userId: shown.device.user_id,
            profileId: shown.device.profile_id,
            deviceId: shown.device.device_id,
            pauseMonitors,
          }
        : { ids: shown.entries.map((e) => e.id) };
    revoke.mutate(
      { target, reason: reason.trim() },
      {
        onSuccess: (result) => {
          toast.success(
            result.revoked > 0
              ? t("web.admin.downloads.deviceCopies.revokeDialog.revoked", {
                  ...countValues(result.revoked),
                  device:
                    shown.device.device_name ||
                    t("web.admin.downloads.deviceCopies.revokeDialog.toastDevice"),
                })
              : t("web.admin.downloads.deviceCopies.revokeDialog.alreadyRevoked"),
          );
          onClose();
        },
        onError: () => toast.error(t("web.admin.downloads.deviceCopies.revokeDialog.failed")),
      },
    );
  }

  return (
    <AlertDialog
      open={intent !== null}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {only
              ? t("web.admin.downloads.deviceCopies.revokeDialog.titleEntry", {
                  title: entryTitle(t, only),
                  device: titleDevice,
                })
              : t("web.admin.downloads.deviceCopies.revokeDialog.titleCount", {
                  ...countValues(count),
                  device: titleDevice,
                })}
          </AlertDialogTitle>
          <AlertDialogDescription>
            {device?.stale
              ? t("web.admin.downloads.deviceCopies.revokeDialog.withStale", {
                  description,
                  stale: seen
                    ? t("web.admin.downloads.deviceCopies.revokeDialog.staleSeen", { when: seen })
                    : t("web.admin.downloads.deviceCopies.revokeDialog.staleUnknown"),
                })
              : description}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="bg-card rounded-xl border p-3 text-sm">
          <div className="font-medium">
            {t("web.admin.downloads.deviceCopies.revokeDialog.frees", {
              size: formatStorageBytes(bytes),
            })}
          </div>
          <div className="text-muted-foreground text-xs">
            {cacheHours === undefined
              ? t("web.admin.downloads.deviceCopies.revokeDialog.cacheUnknown")
              : t(
                  "web.admin.downloads.deviceCopies.revokeDialog.cacheHours",
                  countValues(cacheHours),
                )}
          </div>
        </div>
        {shown?.kind === "device" && shown.device.monitors > 0 ? (
          <label htmlFor={pauseId} className="flex items-start gap-2 text-sm">
            <input
              id={pauseId}
              type="checkbox"
              className={cn(CHECKBOX, "mt-0.5")}
              checked={pauseMonitors}
              onChange={(event) => setPauseMonitors(event.target.checked)}
            />
            <span>
              {t(
                "web.admin.downloads.deviceCopies.revokeDialog.pauseMonitors",
                countValues(shown.device.monitors),
              )}
              <span className="text-muted-foreground block text-xs">
                {t("web.admin.downloads.deviceCopies.revokeDialog.pauseHint")}
              </span>
            </span>
          </label>
        ) : null}
        <div className="space-y-1.5">
          <label htmlFor={reasonId} className="text-sm font-medium">
            {t("web.admin.downloads.deviceCopies.revokeDialog.reason", {
              hint: (chunks) => <span className="text-muted-foreground font-normal">{chunks}</span>,
            })}
          </label>
          <Input
            id={reasonId}
            value={reason}
            maxLength={500}
            onChange={(event) => setReason(event.target.value)}
            placeholder={t("web.admin.downloads.deviceCopies.revokeDialog.reasonPlaceholder")}
          />
        </div>
        <p className="text-muted-foreground text-xs">
          {t("web.admin.downloads.deviceCopies.revokeDialog.oldApps")}
        </p>
        <AlertDialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="destructive"
            disabled={revoke.isPending || count === 0}
            onClick={confirm}
          >
            {revoke.isPending
              ? t("web.admin.downloads.deviceCopies.revokeDialog.revoking")
              : t("web.admin.downloads.deviceCopies.revokeDialog.confirm", countValues(count))}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
