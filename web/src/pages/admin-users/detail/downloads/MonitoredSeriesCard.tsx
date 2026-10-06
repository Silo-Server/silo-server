import type { AdminUserDownloadSubscription } from "@/api/v2/adminUserActivity";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

import { formatStorageLimit } from "../format";
import { DetailCard } from "../ui";
import { monitorKeepsLabel, monitorNowLabel } from "./downloadPresentation";

const wideOnly = "hidden sm:table-cell";

export function MonitoredSeriesCard({
  monitors,
  deviceName,
  profileNames,
  filtered = false,
}: {
  monitors: AdminUserDownloadSubscription[];
  deviceName: (deviceId: string) => string;
  profileNames: Map<string, string>;
  /** A device or profile filter is narrowing the list. */
  filtered?: boolean;
}) {
  return (
    <DetailCard
      title="Monitored series"
      description="New episodes download to the device automatically. Devices check when the app opens or refreshes in the background."
    >
      {monitors.length === 0 ? (
        <p className="text-muted-foreground px-5 py-6 text-center text-sm">
          {filtered
            ? "No monitored series match these filters."
            : "No series are monitored on this account’s devices."}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="pl-4 sm:pl-5">Series</TableHead>
                <TableHead className={wideOnly}>Device · profile</TableHead>
                <TableHead>Keeps</TableHead>
                <TableHead className={wideOnly}>After watching</TableHead>
                <TableHead className={wideOnly}>Storage limit</TableHead>
                <TableHead className="pr-4 sm:pr-5">Now</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {monitors.map((monitor) => (
                <TableRow key={monitor.id}>
                  <TableCell className="pl-4 font-semibold whitespace-normal sm:pl-5">
                    {monitor.series_title || monitor.series_id}
                  </TableCell>
                  <TableCell className={`${wideOnly} text-muted-foreground`}>
                    {deviceName(monitor.device_id)} ·{" "}
                    {profileNames.get(monitor.profile_id) || monitor.profile_id}
                  </TableCell>
                  <TableCell className="whitespace-normal">{monitorKeepsLabel(monitor)}</TableCell>
                  <TableCell className={`${wideOnly} text-muted-foreground`}>
                    {monitor.delete_watched ? "Delete after watching" : "Keep"}
                  </TableCell>
                  <TableCell className={`${wideOnly} text-muted-foreground`}>
                    {monitor.max_storage_bytes > 0
                      ? formatStorageLimit(monitor.max_storage_bytes)
                      : "No limit"}
                  </TableCell>
                  <TableCell className="text-muted-foreground pr-4 whitespace-normal sm:pr-5">
                    {monitorNowLabel(monitor)}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </DetailCard>
  );
}
