import type { AdminUserDownloadSubscription } from "@/api/v2/adminUserActivity";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useT } from "@/i18n";

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
  const t = useT();
  return (
    <DetailCard
      title={t("web.admin.users.detail.downloads.monitoredSeries")}
      description={t("web.admin.users.detail.downloads.monitor.description")}
    >
      {monitors.length === 0 ? (
        <p className="text-muted-foreground px-5 py-6 text-center text-sm">
          {filtered
            ? t("web.admin.users.detail.downloads.monitor.noMatch")
            : t("web.admin.users.detail.downloads.monitor.empty")}
        </p>
      ) : (
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="ps-4 sm:ps-5">
                  {t("web.admin.users.detail.downloads.monitor.column.series")}
                </TableHead>
                <TableHead className={wideOnly}>
                  {t("web.admin.users.detail.downloads.monitor.column.deviceProfile")}
                </TableHead>
                <TableHead>{t("web.admin.users.detail.downloads.monitor.column.keeps")}</TableHead>
                <TableHead className={wideOnly}>
                  {t("web.admin.users.detail.downloads.monitor.column.afterWatching")}
                </TableHead>
                <TableHead className={wideOnly}>
                  {t("web.admin.users.detail.downloads.monitor.column.storageLimit")}
                </TableHead>
                <TableHead className="pe-4 sm:pe-5">
                  {t("web.admin.users.detail.downloads.monitor.column.now")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {monitors.map((monitor) => (
                <TableRow key={monitor.id}>
                  <TableCell className="ps-4 font-semibold whitespace-normal sm:ps-5">
                    {monitor.series_title || monitor.series_id}
                  </TableCell>
                  <TableCell className={`${wideOnly} text-muted-foreground`}>
                    {[
                      deviceName(monitor.device_id),
                      profileNames.get(monitor.profile_id) || monitor.profile_id,
                    ].join(" · ")}
                  </TableCell>
                  <TableCell className="whitespace-normal">{monitorKeepsLabel(monitor)}</TableCell>
                  <TableCell className={`${wideOnly} text-muted-foreground`}>
                    {monitor.delete_watched
                      ? t("web.admin.users.detail.downloads.monitor.deleteAfterWatching")
                      : t("web.admin.users.detail.downloads.monitor.keep")}
                  </TableCell>
                  <TableCell className={`${wideOnly} text-muted-foreground`}>
                    {monitor.max_storage_bytes > 0
                      ? formatStorageLimit(monitor.max_storage_bytes)
                      : t("web.admin.users.detail.downloads.monitor.noLimit")}
                  </TableCell>
                  <TableCell className="text-muted-foreground pe-4 whitespace-normal sm:pe-5">
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
