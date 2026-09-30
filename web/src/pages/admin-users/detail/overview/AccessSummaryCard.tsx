import type { ReactNode } from "react";
import { Link } from "react-router";
import { Download, LayoutGrid, Mail, Play } from "lucide-react";

import type { AdminUser } from "@/api/types";
import { Button } from "@/components/ui/button";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminUserCapabilities } from "@/hooks/queries/admin/users";
import { useAdminUserDownloadSummary } from "@/hooks/queries/admin/userActivity";
import {
  PERMISSION_MARKER_EDIT,
  PERMISSION_METADATA_CURATION,
  hasAssignedPermission,
} from "@/lib/permissions";
import { formatStreamBitrateLimit } from "@/lib/streamBitrateLimit";

import { useAccountRequestTerms } from "../access/RequestsCard";
import {
  countCustomPolicyRows,
  formatQuality,
  inheritContextFor,
  libraryListText,
  videoTranscodingFromEffective,
} from "../access/policySources";
import { userDetailTabSearch } from "../userDetailTabs";
import { DetailCard, SourceTag } from "../ui";

function plural(n: number, one: string, many: string) {
  return `${n} ${n === 1 ? one : many}`;
}

function SummaryLine({
  icon,
  title,
  detail,
}: {
  icon: ReactNode;
  title: ReactNode;
  detail?: ReactNode;
}) {
  return (
    <div className="border-border/50 flex items-start gap-3 border-t px-4 py-3 first:border-t-0 sm:px-5">
      <span
        aria-hidden="true"
        className="bg-accent text-muted-foreground mt-0.5 inline-flex size-7 shrink-0 items-center justify-center rounded-md [&_svg]:size-3.5"
      >
        {icon}
      </span>
      <div className="min-w-0">
        <div className="text-sm font-medium">{title}</div>
        {detail ? (
          <div className="text-muted-foreground text-xs leading-relaxed">{detail}</div>
        ) : null}
      </div>
    </div>
  );
}

const CUSTOM = (
  <span className="ml-1.5 inline-block align-middle">
    <SourceTag source="custom" />
  </span>
);

/** A few lines on what the account can do, with the account's own limits marked. */
export function AccessSummaryCard({ user }: { user: AdminUser }) {
  const groups = useAccessGroups().data ?? [];
  const libraries = useAdminLibraries().data ?? [];
  const capabilities = useAdminUserCapabilities().data;
  const accountDownloads = capabilities?.account_downloads === true;
  const downloads = useAdminUserDownloadSummary(user.id, accountDownloads);
  const groupName = groups.find((group) => group.id === user.access_group_id)?.name;
  const requests = useAccountRequestTerms(user, groupName);
  const effective = user.effective_policy;
  const ctx = inheritContextFor(user, groups);
  const custom = countCustomPolicyRows(user);
  const source = ctx.kind === "group" ? `${ctx.name} group` : "Server default";

  const marker = hasAssignedPermission(effective.permissions, PERMISSION_MARKER_EDIT);
  const curate = hasAssignedPermission(effective.permissions, PERMISSION_METADATA_CURATION);
  const video = videoTranscodingFromEffective(
    effective.transcode_allowed,
    effective.max_transcodes,
  );
  const videoText =
    video.mode === "off"
      ? "No video transcoding"
      : video.mode === "unlimited"
        ? "Unlimited video transcodes"
        : `Up to ${plural(video.max, "video transcode", "video transcodes")}`;
  const videoCustom = user.transcode_allowed !== null || user.max_transcodes !== null;
  const remote = effective.max_remote_stream_bitrate_kbps;

  const summary = downloads.data;
  const downloadsDetail = [
    effective.download_transcode_allowed ? "Server-prepared copies allowed" : "Original files only",
    ...(accountDownloads && summary
      ? [
          `${plural(summary.completed, "download", "downloads")} confirmed on devices`,
          `${plural(summary.monitored_series, "series", "series")} monitored`,
        ]
      : []),
  ].join(" · ");

  const { terms, server } = requests;
  const canRequest = effective.requests_allowed && server?.requests_enabled !== false;
  const requestsTitle = !canRequest
    ? "Can't request media"
    : !terms
      ? "Requests"
      : terms.quota.unlimited
        ? "Unlimited requests"
        : `${plural(terms.quota.max, "request", "requests")} per ${terms.quota.days === 1 ? "day" : `${terms.quota.days} days`}`;

  return (
    <DetailCard
      title="Access"
      description={
        custom > 0
          ? `${source}, with ${plural(custom, "limit", "limits")} set for this account`
          : source
      }
      actions={
        <Button asChild variant="outline" size="xs">
          <Link to={userDetailTabSearch("access")}>Access &amp; limits →</Link>
        </Button>
      }
    >
      <SummaryLine
        icon={<LayoutGrid />}
        title={libraryListText(effective.library_ids, libraries)}
        detail={`${marker ? "Can" : "Can't"} edit markers · ${curate ? "can" : "can't"} curate metadata`}
      />
      <SummaryLine
        icon={<Play />}
        title={
          <>
            {formatQuality(effective.max_playback_quality)} quality
            {user.max_playback_quality !== null ? CUSTOM : null} ·{" "}
            {effective.max_streams === 0
              ? "unlimited streams"
              : plural(effective.max_streams, "stream", "streams")}
            {user.max_streams !== null ? CUSTOM : null}
          </>
        }
        detail={
          <>
            {videoText}
            {videoCustom ? CUSTOM : null} ·{" "}
            {remote === 0 ? "no bitrate cap" : `${formatStreamBitrateLimit(remote)} remote cap`}
          </>
        }
      />
      <SummaryLine
        icon={<Download />}
        title={effective.download_allowed ? "Downloads allowed" : "Downloads not allowed"}
        detail={downloadsDetail}
      />
      <SummaryLine
        icon={<Mail />}
        title={requestsTitle}
        detail={
          canRequest && terms
            ? terms.autoApprove
              ? "Approved automatically"
              : "Needs approval"
            : undefined
        }
      />
    </DetailCard>
  );
}
