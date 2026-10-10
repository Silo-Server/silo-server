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
import { formatRelativeTime } from "@/lib/date";
import { cn } from "@/lib/utils";
import { CHECKBOX, LoadMoreButton, SearchField } from "./controls";
import { formatStorageBytes } from "./downloadStoragePresentation";

type Sort = "last_seen" | "size";

/** What a revoke dialog is about: a whole device, or some of its downloads. */
type RevokeIntent =
  | { kind: "device"; device: AdminDownloadDevice }
  | { kind: "entries"; device: AdminDownloadDevice; entries: AdminDownloadEntry[] };

function deviceKey(device: AdminDownloadDevice) {
  return `${device.user_id}/${device.profile_id}/${device.device_id}`;
}

export default function DeviceCopiesTab({ initialStale = false }: { initialStale?: boolean }) {
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
        <SearchField label="Search people or devices" className="w-56" onSearch={setQuery} />
        <Button
          variant="outline"
          size="sm"
          aria-pressed={stale}
          className={cn(stale && "border-warning/50 bg-warning/10 text-warning hover:text-warning")}
          onClick={() => setStale((value) => !value)}
        >
          Not seen in {staleDays}+ days
        </Button>
        <Select value={sort} onValueChange={(value) => setSort(value as Sort)}>
          <SelectTrigger size="sm" className="w-52" aria-label="Sort">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="last_seen">Longest unseen first</SelectItem>
            <SelectItem value="size">Most on the device first</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div className="surface-panel overflow-hidden rounded-2xl">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8" />
              <TableHead>Device</TableHead>
              <TableHead>Person</TableHead>
              <TableHead>Last seen</TableHead>
              <TableHead className="hidden sm:table-cell">Copies</TableHead>
              <TableHead className="text-right">On device</TableHead>
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
                    ? "Devices could not be read."
                    : stale
                      ? "No stale devices hold downloads."
                      : "No device holds downloads."}
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
                              ? `Hide downloads on ${device.device_name}`
                              : `Show downloads on ${device.device_name}`
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
                          {device.platform || "Unknown platform"}
                        </div>
                      </TableCell>
                      <TableCell>
                        <div>{device.profile_name || device.profile_id}</div>
                        <div className="text-muted-foreground text-xs">{device.username}</div>
                      </TableCell>
                      <TableCell
                        className={cn("whitespace-nowrap", device.stale && "text-warning")}
                      >
                        {formatRelativeTime(device.last_seen_at) ?? "Never"}
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        <div>
                          {device.copies}{" "}
                          <span className="text-muted-foreground">
                            ·{" "}
                            {[
                              device.finished && `${device.finished} finished`,
                              device.waiting && `${device.waiting} waiting`,
                              device.failed && `${device.failed} failed`,
                            ]
                              .filter(Boolean)
                              .join(", ") || "none active"}
                          </span>
                        </div>
                        {device.revoked > 0 ? (
                          <div className="text-warning text-xs">
                            {device.revoked} revoked, waiting for device
                          </div>
                        ) : device.monitors > 0 ? (
                          <div className="text-muted-foreground text-xs">
                            {device.monitors} series monitored
                          </div>
                        ) : null}
                      </TableCell>
                      <TableCell className="text-right whitespace-nowrap tabular-nums">
                        {formatStorageBytes(device.bytes_on_device)}
                      </TableCell>
                      <TableCell className="text-right">
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={device.copies === 0}
                          aria-label={`Revoke all downloads on ${device.device_name || device.device_id}`}
                          onClick={() => setRevoking({ kind: "device", device })}
                        >
                          Revoke all…
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

function entryTitle(entry: AdminDownloadEntry) {
  if (entry.episode) {
    return `${entry.title || "Unknown series"} · S${entry.episode.season_number} E${entry.episode.episode_number}`;
  }
  return entry.title || "Unknown title";
}

/** "Original", or a bitrate preset such as "10mbps" as "10 Mbps". */
function qualityLabel(quality: string) {
  if (quality === "original") return "Original";
  const mbps = /^(\d+)mbps$/.exec(quality);
  return mbps ? `${mbps[1]} Mbps` : quality;
}

function entryStatus(entry: AdminDownloadEntry) {
  switch (entry.status) {
    case "completed":
      return { label: "Finished", className: "bg-muted text-foreground" };
    case "queued":
    case "preparing":
    case "ready":
      return { label: "Waiting", className: "text-foreground bg-chart-1/30" };
    case "downloading":
      return { label: "Downloading", className: "text-foreground bg-chart-1/30" };
    case "revoked":
      return { label: "Revoked", className: "text-warning bg-warning/10" };
    case "failed":
      return { label: "Failed", className: "text-destructive bg-destructive/10" };
    case "cancelled":
      return { label: "Cancelled", className: "bg-muted text-muted-foreground" };
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
    <div className="space-y-2 px-3 py-3 sm:pl-12">
      {chosen.length > 0 ? (
        <div className="flex items-center gap-2 text-xs">
          <span>{chosen.length} selected</span>
          <Button
            size="sm"
            variant="outline"
            className="h-7 text-xs"
            onClick={() => onRevoke(chosen)}
          >
            Revoke {chosen.length}…
          </Button>
        </div>
      ) : null}
      <ul className="divide-border/60 divide-y text-sm">
        {rows.map((row) => {
          const status = entryStatus(row);
          return (
            <li key={row.id} className="flex flex-wrap items-center gap-3 py-2">
              <input
                type="checkbox"
                className={CHECKBOX}
                aria-label={`Select ${entryTitle(row)}`}
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
                <div className="truncate font-medium">{entryTitle(row)}</div>
                <div className="text-muted-foreground text-xs">
                  {[
                    row.episode?.title,
                    qualityLabel(row.effective_quality),
                    row.location_name && `prepared on ${row.location_name}`,
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
              <span className="w-20 text-right tabular-nums">
                {formatStorageBytes(row.file_size)}
              </span>
              {row.status !== "revoked" ? (
                <button
                  type="button"
                  className="text-xs underline underline-offset-3"
                  aria-label={`Revoke ${entryTitle(row)}`}
                  onClick={() => onRevoke([row])}
                >
                  Revoke
                </button>
              ) : (
                <span className="text-muted-foreground w-12 text-xs">sent</span>
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
          {entries.isFetchingNextPage ? "Loading…" : "Show more"}
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
  const subject = only
    ? `“${entryTitle(only)}”`
    : `${count} ${count === 1 ? "download" : "downloads"}`;

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
              ? `Revoked ${result.revoked} ${result.revoked === 1 ? "download" : "downloads"} on ${shown.device.device_name || "the device"}`
              : "Those downloads were already revoked",
          );
          onClose();
        },
        onError: () => toast.error("The downloads could not be revoked."),
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
            Revoke {subject} on {device?.device_name || "this device"}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            The Silo app on {device?.profile_name ? `${device.profile_name}'s ` : "the "}
            {device?.device_name || "device"} deletes {count === 1 ? "it" : "them"} the next time it
            connects.
            {device?.stale
              ? ` It was last seen ${seen ?? "a long time ago"}, so this may take a while.`
              : ""}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <div className="bg-card rounded-xl border p-3 text-sm">
          <div className="font-medium">Frees {formatStorageBytes(bytes)} on the device.</div>
          <div className="text-muted-foreground text-xs">
            Prepared files on the server that only these downloads were waiting on become cached and
            expire after {cacheHours === undefined ? "the cache period" : `${cacheHours} hours`}.
            Preparations nothing else needs are canceled.
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
              Also pause the{" "}
              {shown.device.monitors === 1
                ? "series monitor"
                : `${shown.device.monitors} series monitors`}{" "}
              on this device
              <span className="text-muted-foreground block text-xs">
                Otherwise new episodes download again.
              </span>
            </span>
          </label>
        ) : null}
        <div className="space-y-1.5">
          <label htmlFor={reasonId} className="text-sm font-medium">
            Reason{" "}
            <span className="text-muted-foreground font-normal">(optional, kept in History)</span>
          </label>
          <Input
            id={reasonId}
            value={reason}
            maxLength={500}
            onChange={(event) => setReason(event.target.value)}
            placeholder="e.g. Device lost"
          />
        </div>
        <p className="text-muted-foreground text-xs">
          Apps released before revocation support keep files they already downloaded.
        </p>
        <AlertDialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            disabled={revoke.isPending || count === 0}
            onClick={confirm}
          >
            {revoke.isPending
              ? "Revoking…"
              : `Revoke ${count === 1 ? "download" : `${count} downloads`}`}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
