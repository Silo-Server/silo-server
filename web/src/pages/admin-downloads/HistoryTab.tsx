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
const DAY_OPTIONS = [
  { value: 1, label: "Last day" },
  { value: 7, label: "Last 7 days" },
  { value: 30, label: "Last 30 days" },
  { value: 0, label: "All kept history" },
];

export default function HistoryTab() {
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
          label="Freed, 30 days"
          value={settings ? formatStorageBytes(settings.freed_last_30_days_bytes) : "—"}
          detail="server and nodes"
        />
        <StatTile
          label="Keep cached files for"
          value={settings ? `${settings.cache_hours} h` : "—"}
          detail="after their last use"
        />
        <StatTile
          label="Disk ceiling"
          value={settings ? `${settings.disk_ceiling_percent}%` : "—"}
          detail="cached files go early above it"
        />
        <StatTile
          label="History kept"
          value="90 days"
          detail="one row per clean-up pass or action"
        />
      </StatStrip>

      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={reason || ALL}
          onValueChange={(value) => setReason(value === ALL ? "" : value)}
        >
          <SelectTrigger size="sm" className="w-52" aria-label="Reason">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>Every reason</SelectItem>
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
          <SelectTrigger size="sm" className="w-44" aria-label="Location">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>All locations</SelectItem>
            {locations.map((l) => (
              <SelectItem key={l.key} value={l.key}>
                {l.name}
              </SelectItem>
            ))}
            <SelectItem value="device">Devices</SelectItem>
          </SelectContent>
        </Select>
        <Select value={String(days)} onValueChange={(value) => setDays(Number(value))}>
          <SelectTrigger size="sm" className="w-40" aria-label="When">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {DAY_OPTIONS.map((option) => (
              <SelectItem key={option.value} value={String(option.value)}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <div className="surface-panel overflow-hidden rounded-2xl">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>When</TableHead>
              <TableHead>What happened</TableHead>
              <TableHead>Item</TableHead>
              <TableHead className="hidden sm:table-cell">Location</TableHead>
              <TableHead className="text-right">Size</TableHead>
              <TableHead className="hidden md:table-cell">By</TableHead>
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
                  {events.isError
                    ? "History could not be read."
                    : "Nothing was cleaned up or revoked in this period."}
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
                      <div className="text-warning text-xs">waiting for device</div>
                    ) : null}
                  </TableCell>
                  <TableCell className="hidden sm:table-cell">
                    {storageEventLocation(event)}
                  </TableCell>
                  <TableCell className="text-right whitespace-nowrap tabular-nums">
                    {formatStorageBytes(event.bytes)}
                  </TableCell>
                  <TableCell className="text-muted-foreground hidden md:table-cell">
                    {event.actor?.username ??
                      (event.reason === "device_removed" ? "Device confirmed" : "Automatic")}
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
