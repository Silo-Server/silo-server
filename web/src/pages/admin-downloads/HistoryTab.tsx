import { useMemo, useState } from "react";
import type { AdminDownloadStorageEventsQuery } from "@/api/v2/adminDownloadStorage";
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
  useAdminDownloadStorage,
  useAdminDownloadStorageEvents,
} from "@/hooks/queries/admin/downloadStorage";
import { useAppLanguage, useT } from "@/i18n";
import { formatDateTime } from "@/lib/datetime";
import { StatStrip, StatTile } from "@/pages/admin-users/detail/ui";
import {
  STORAGE_EVENT_REASONS,
  formatStorageBytes,
  storageEventItem,
  storageEventLocation,
  storageEventReasonLabel,
} from "./downloadStoragePresentation";
import { LoadMoreButton } from "./controls";

const ALL = "all";
/** Days of history to show; 0 shows every kept row. */
const DAY_OPTIONS = [1, 7, 30, 0];
const H = "web.admin.downloads.history";

export default function HistoryTab() {
  const t = useT();
  // The reason, item and location labels come from plain helpers.
  useAppLanguage();
  const storage = useAdminDownloadStorage();
  const [reason, setReason] = useState("");
  const [location, setLocation] = useState("");
  const [days, setDays] = useState(30);
  const query = useMemo<Omit<AdminDownloadStorageEventsQuery, "cursor">>(
    () => ({ reason: reason || undefined, location: location || undefined, days }),
    [reason, location, days],
  );
  const events = useAdminDownloadStorageEvents(query);
  const rows = events.data?.pages.flatMap((page) => page.items) ?? [];
  const locations = storage.data?.locations ?? [];
  const settings = storage.data;

  return (
    <div className="space-y-4">
      <StatStrip columns={4}>
        <StatTile
          label={t(`${H}.freed`)}
          value={settings ? formatStorageBytes(settings.freed_last_30_days_bytes) : "—"}
          detail={t(`${H}.freedDetail`)}
        />
        <StatTile
          label={t(`${H}.cacheFor`)}
          value={
            settings
              ? t(`${H}.cacheHours`, {
                  count: settings.cache_hours,
                  count_text: settings.cache_hours,
                })
              : "—"
          }
          detail={t(`${H}.cacheDetail`)}
        />
        <StatTile
          label={t(`${H}.diskCeiling`)}
          value={
            settings ? t(`${H}.diskCeilingValue`, { percent: settings.disk_ceiling_percent }) : "—"
          }
          detail={t(`${H}.diskCeilingDetail`)}
        />
        <StatTile
          label={t(`${H}.kept`)}
          value={t(`${H}.keptValue`, { count: 90 })}
          detail={t(`${H}.keptDetail`)}
        />
      </StatStrip>

      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={reason || ALL}
          onValueChange={(value) => setReason(value === ALL ? "" : value)}
        >
          <SelectTrigger size="sm" className="w-52" aria-label={t(`${H}.reasonLabel`)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t(`${H}.everyReason`)}</SelectItem>
            {STORAGE_EVENT_REASONS.map((value) => (
              <SelectItem key={value} value={value}>
                {storageEventReasonLabel(value)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={location || ALL}
          onValueChange={(value) => setLocation(value === ALL ? "" : value)}
        >
          <SelectTrigger size="sm" className="w-44" aria-label={t(`${H}.locationLabel`)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t(`${H}.allLocations`)}</SelectItem>
            {locations.map((l) => (
              <SelectItem key={l.key} value={l.key}>
                {l.name}
              </SelectItem>
            ))}
            <SelectItem value="device">{t(`${H}.devices`)}</SelectItem>
          </SelectContent>
        </Select>
        <Select value={String(days)} onValueChange={(value) => setDays(Number(value))}>
          <SelectTrigger size="sm" className="w-40" aria-label={t(`${H}.whenLabel`)}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {DAY_OPTIONS.map((value) => (
              <SelectItem key={value} value={String(value)}>
                {value === 0 ? t(`${H}.allKept`) : t(`${H}.lastDays`, { count: value })}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <div className="surface-panel overflow-hidden rounded-2xl">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t(`${H}.column.when`)}</TableHead>
              <TableHead>{t(`${H}.column.whatHappened`)}</TableHead>
              <TableHead>{t(`${H}.column.item`)}</TableHead>
              <TableHead className="hidden sm:table-cell">{t(`${H}.column.location`)}</TableHead>
              <TableHead className="text-end">{t(`${H}.column.size`)}</TableHead>
              <TableHead className="hidden md:table-cell">{t(`${H}.column.by`)}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {events.isLoading ? (
              Array.from({ length: 4 }, (_, i) => (
                <TableRow key={i}>
                  <TableCell colSpan={6}>
                    <Skeleton className="h-6" />
                  </TableCell>
                </TableRow>
              ))
            ) : rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={6} className="text-muted-foreground py-10 text-center">
                  {events.isError ? t(`${H}.loadFailed`) : t(`${H}.empty`)}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((event) => (
                <TableRow key={event.id}>
                  <TableCell className="whitespace-nowrap">
                    {formatDateTime(event.occurred_at)}
                  </TableCell>
                  <TableCell>{storageEventReasonLabel(event.reason)}</TableCell>
                  <TableCell>
                    <div>{storageEventItem(event)}</div>
                    {event.detail && event.reason !== "untracked" ? (
                      <div className="text-muted-foreground text-xs">{event.detail}</div>
                    ) : event.reason === "revoked" ? (
                      <div className="text-warning text-xs">{t(`${H}.waitingForDevice`)}</div>
                    ) : null}
                  </TableCell>
                  <TableCell className="hidden sm:table-cell">
                    {storageEventLocation(event)}
                  </TableCell>
                  <TableCell className="text-end whitespace-nowrap tabular-nums">
                    {formatStorageBytes(event.bytes)}
                  </TableCell>
                  <TableCell className="text-muted-foreground hidden md:table-cell">
                    {event.actor?.username ??
                      (event.reason === "device_removed"
                        ? t(`${H}.deviceConfirmed`)
                        : t(`${H}.automatic`))}
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </div>
      <LoadMoreButton query={events} />
    </div>
  );
}
