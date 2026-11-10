import { useState } from "react";
import { Link } from "react-router";
import { ArrowUpRight, ChevronDown, ChevronRight } from "lucide-react";

import type {
  AdminUserDeviceRow,
  AdminUserDownload,
  AdminUserDownloadSubscription,
} from "@/api/v2/adminUserActivity";
import {
  PlatformTile,
  classifyPlatform,
  platformLabel,
  shortenId,
} from "@/components/admin/deviceOverrides";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useT, type MessageKey } from "@/i18n";
import { cn } from "@/lib/utils";

import { episodeCode, formatBytes, formatLastSeen, formatShortDate } from "../format";
import { DetailCard } from "../ui";
import {
  IN_PROGRESS_STATUSES,
  downloadQualityLabel,
  downloadStatusBadge,
  groupDeviceDownloads,
  isAndroidPlatform,
  seriesGroupStatus,
  seriesGroupSubtitle,
  type DownloadBadge,
  type DownloadGroup,
} from "./downloadPresentation";

const TONE_CLASSES: Record<DownloadBadge["tone"], string> = {
  ok: "border-emerald-500/35 bg-emerald-500/10 text-emerald-300",
  accent: "border-amber-500/40 bg-amber-500/10 text-amber-200",
  neutral: "border-border bg-background/60 text-foreground",
  warn: "border-yellow-500/40 bg-yellow-500/10 text-yellow-200",
  danger: "border-destructive/40 bg-destructive/10 text-red-300",
};

function StatusBadge({ badge }: { badge: DownloadBadge }) {
  return (
    <Badge
      variant="outline"
      data-tone={badge.tone}
      className={cn("rounded-full", TONE_CLASSES[badge.tone])}
    >
      {badge.tone === "ok" && (
        <span aria-hidden className="h-1.5 w-1.5 rounded-full bg-emerald-400" />
      )}
      {badge.label}
    </Badge>
  );
}

function latest(rows: AdminUserDownload[]): string {
  return rows.map((row) => row.created_at).sort((a, b) => b.localeCompare(a))[0] ?? "";
}

function sharedQuality(rows: AdminUserDownload[], mixed: string): string {
  const labels = new Set(rows.map(downloadQualityLabel));
  return labels.size === 1 ? [...labels][0]! : mixed;
}

function totalSize(rows: AdminUserDownload[]): number {
  return rows.reduce((sum, row) => sum + row.file_size, 0);
}

/** "S01E02 · Title", or whichever half is known. */
function episodeLabel(episode: NonNullable<AdminUserDownload["episode"]>): string {
  return [episodeCode(episode.season_number, episode.episode_number), episode.title]
    .filter(Boolean)
    .join(" · ");
}

const sizeCell = "text-muted-foreground text-end tabular-nums";
const mutedCell = "text-muted-foreground hidden sm:table-cell";

function GroupRows({ group, android }: { group: DownloadGroup; android: boolean }) {
  const t = useT();
  // A series with work outstanding opens by default so the admin sees what's left.
  const [open, setOpen] = useState(() =>
    group.episodes.some((episode) => episode.status !== "completed"),
  );
  const monitored = group.monitored ? (
    <Badge variant="outline" className="ms-2 rounded-full">
      {t("web.admin.users.detail.downloads.device.monitored")}
    </Badge>
  ) : null;

  if (group.movie || group.episodes.length === 1) {
    const row = group.movie ?? group.episodes[0]!;
    const code = row.episode ? episodeLabel(row.episode) : "";
    return (
      <TableRow>
        <TableCell className="ps-4 whitespace-normal sm:ps-5">
          <span className="font-semibold">{group.title}</span>
          {monitored}
          {code ? <div className="text-muted-foreground text-xs">{code}</div> : null}
        </TableCell>
        <TableCell className={mutedCell}>{downloadQualityLabel(row)}</TableCell>
        <TableCell className={sizeCell}>{formatBytes(row.file_size)}</TableCell>
        <TableCell>
          <StatusBadge badge={downloadStatusBadge(row.status, android)} />
        </TableCell>
        <TableCell className={cn(mutedCell, "pe-4 sm:pe-5")}>
          {formatShortDate(row.created_at)}
        </TableCell>
      </TableRow>
    );
  }

  return (
    <>
      <TableRow>
        <TableCell className="ps-4 whitespace-normal sm:ps-5">
          <button
            type="button"
            aria-expanded={open}
            onClick={() => setOpen((value) => !value)}
            className="hover:text-foreground -ms-1 inline-flex items-center gap-1 text-start font-semibold"
          >
            {open ? (
              <ChevronDown aria-hidden className="h-3.5 w-3.5" />
            ) : (
              <ChevronRight aria-hidden className="h-3.5 w-3.5" />
            )}
            {group.title}
          </button>
          {monitored}
          <div className="text-muted-foreground ps-4 text-xs">{seriesGroupSubtitle(group)}</div>
        </TableCell>
        <TableCell className={mutedCell}>
          {sharedQuality(group.episodes, t("web.admin.users.detail.downloads.quality.mixed"))}
        </TableCell>
        <TableCell className={sizeCell}>{formatBytes(totalSize(group.episodes))}</TableCell>
        <TableCell>
          <StatusBadge badge={seriesGroupStatus(group, android)} />
        </TableCell>
        <TableCell className={cn(mutedCell, "pe-4 sm:pe-5")}>
          {formatShortDate(latest(group.episodes))}
        </TableCell>
      </TableRow>
      {open &&
        group.episodes.map((row) => (
          <TableRow key={row.id}>
            <TableCell className="text-muted-foreground ps-10 whitespace-normal sm:ps-11">
              {row.episode
                ? episodeLabel(row.episode)
                : t("web.admin.users.detail.downloads.unknownEpisode")}
            </TableCell>
            <TableCell className={mutedCell} />
            <TableCell className={sizeCell}>{formatBytes(row.file_size)}</TableCell>
            <TableCell>
              <StatusBadge badge={downloadStatusBadge(row.status, android)} />
            </TableCell>
            <TableCell className={cn(mutedCell, "pe-4 sm:pe-5")}>
              {formatShortDate(row.created_at)}
            </TableCell>
          </TableRow>
        ))}
    </>
  );
}

export function DeviceDownloadsCard({
  userId,
  deviceId,
  device,
  rows,
  summaryRows = rows,
  monitors,
  profileNames,
}: {
  userId: number;
  deviceId: string;
  /** Undefined when the device is not in the account's device list. */
  device: AdminUserDeviceRow | undefined;
  /** The rows the table shows, after the page's filters. */
  rows: AdminUserDownload[];
  /** Every row on the device, for the header totals; defaults to `rows`. */
  summaryRows?: AdminUserDownload[];
  monitors: AdminUserDownloadSubscription[];
  profileNames: Map<string, string>;
}) {
  const t = useT();
  const android = isAndroidPlatform(device?.device_platform);
  const groups = groupDeviceDownloads(rows, monitors);
  const notRevoked = summaryRows.filter((row) => row.status !== "revoked");
  const onDevice = summaryRows.filter((row) => row.status === "completed").length;
  const inProgress = summaryRows.filter((row) => IN_PROGRESS_STATUSES.has(row.status)).length;
  const requested = summaryRows.filter(
    (row) => row.status === "ready" || row.status === "downloading",
  ).length;
  const preparing = summaryRows.filter((row) => row.status === "preparing").length;
  const profiles = [
    ...new Set(summaryRows.map((row) => profileNames.get(row.profile_id) || row.profile_id)),
  ];
  const profileMeta =
    profiles.length > 0
      ? t("web.admin.users.detail.downloads.device.profiles", { names: profiles.join(", ") })
      : "";

  const meta = device
    ? [
        platformLabel(device.device_platform),
        profileMeta,
        t("web.admin.users.detail.downloads.device.lastSeen", {
          when: formatLastSeen(device.last_seen_at ?? device.last_updated),
        }),
      ]
    : [shortenId(deviceId), profileMeta];
  const counted = (key: MessageKey, n: number) => t(key, { count: n, count_text: String(n) });
  const totals = android
    ? [
        onDevice > 0 ? counted("web.admin.users.detail.downloads.count.onDevice", onDevice) : "",
        counted("web.admin.users.detail.downloads.count.requested", requested),
        preparing > 0 ? counted("web.admin.users.detail.downloads.count.preparing", preparing) : "",
      ]
    : [
        counted("web.admin.users.detail.downloads.count.onDevice", onDevice),
        counted("web.admin.users.detail.downloads.count.inProgress", inProgress),
      ];
  totals.push(formatBytes(totalSize(notRevoked)));

  return (
    <DetailCard
      title={
        <span className="flex items-center gap-3">
          <PlatformTile kind={classifyPlatform(device?.device_platform)} />
          {device
            ? device.device_name || t("web.admin.users.detail.common.unnamedDevice")
            : t("web.admin.users.detail.common.unknownDevice")}
        </span>
      }
      description={<span className="block ps-12">{meta.filter(Boolean).join(" · ")}</span>}
      actions={
        <>
          <span className="text-muted-foreground hidden text-xs tabular-nums sm:inline">
            {totals.filter(Boolean).join(" · ")}
          </span>
          {/* A device no longer registered or holding settings has no detail page. */}
          {device ? (
            <Button variant="outline" size="sm" asChild>
              <Link to={`/admin/devices/${userId}/${encodeURIComponent(deviceId)}`}>
                {t("web.admin.users.detail.downloads.device.openDevice")}
                <ArrowUpRight className="h-3 w-3" />
              </Link>
            </Button>
          ) : null}
        </>
      }
    >
      {android && (
        <p
          role="note"
          className="border-border/60 bg-muted/30 mx-4 mt-3 rounded-lg border px-3 py-2 text-[13px] leading-relaxed sm:mx-5"
        >
          {t("web.admin.users.detail.downloads.device.androidNote")}
        </p>
      )}
      <div className="overflow-x-auto">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="ps-4 sm:ps-5">
                {t("web.admin.users.detail.downloads.device.column.title")}
              </TableHead>
              <TableHead className="hidden sm:table-cell">
                {t("web.admin.users.detail.downloads.device.column.quality")}
              </TableHead>
              <TableHead className="text-end">
                {t("web.admin.users.detail.downloads.device.column.size")}
              </TableHead>
              <TableHead>{t("web.admin.users.detail.downloads.device.column.status")}</TableHead>
              <TableHead className="hidden pe-4 sm:table-cell sm:pe-5">
                {t("web.admin.users.detail.downloads.device.column.added")}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {groups.map((group) => (
              <GroupRows key={group.key} group={group} android={android} />
            ))}
          </TableBody>
        </Table>
      </div>
    </DetailCard>
  );
}
