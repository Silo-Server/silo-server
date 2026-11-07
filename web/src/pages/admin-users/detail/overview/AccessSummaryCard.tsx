import type { ReactNode } from "react";
import { Link } from "react-router";
import { Download, LayoutGrid, Mail, Play } from "lucide-react";

import type { AdminUser } from "@/api/types";
import { Button } from "@/components/ui/button";
import { useAccessGroups } from "@/hooks/queries/admin/accessGroups";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminUserCapabilities } from "@/hooks/queries/admin/users";
import { useAdminUserDownloadSummary } from "@/hooks/queries/admin/userActivity";
import { useT, type Translate } from "@/i18n";
import {
  PERMISSION_MARKER_EDIT,
  PERMISSION_METADATA_CURATION,
  hasAssignedPermission,
} from "@/lib/permissions";
import { playbackQualityPresetFromValue } from "@/lib/playback-quality";
import type { ResolvedRequestTerms } from "@/lib/requestAccess";
import { formatStreamBitrateLimit } from "@/lib/streamBitrateLimit";

import { useAccountRequestTerms } from "../access/RequestsCard";
import {
  countCustomPolicyRows,
  countCustomRequestTerms,
  formatQuality,
  inheritContextFor,
  libraryListText,
  videoTranscodingFromEffective,
  type VideoTranscoding,
} from "../access/policySources";
import { userDetailTabSearch } from "../userDetailTabs";
import { DetailCard, SourceTag } from "../ui";

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

function videoTranscodingText(t: Translate, video: VideoTranscoding): string {
  switch (video.mode) {
    case "off":
      return t("web.admin.users.detail.overview.access.noVideoTranscoding");
    case "unlimited":
      return t("web.admin.users.detail.overview.access.unlimitedVideoTranscodes");
    case "limit":
      return t("web.admin.users.detail.overview.access.videoTranscodeLimit", {
        count: video.max,
        count_text: String(video.max),
      });
  }
}

/** The requests line: whether the account can request, and its quota and approval once known. */
function requestsSummary(
  t: Translate,
  canRequest: boolean,
  terms: ResolvedRequestTerms | undefined,
): { title: ReactNode; detail?: ReactNode } {
  if (!canRequest) return { title: t("web.admin.users.detail.overview.access.cantRequest") };
  if (!terms) return { title: t("web.admin.users.detail.overview.access.requests") };
  const detail = (
    <>
      {terms.autoApprove
        ? t("web.admin.users.detail.overview.access.autoApprove")
        : t("web.admin.users.detail.overview.access.needsApproval")}
      {terms.approvalSource.kind === "account" ? CUSTOM : null}
    </>
  );
  const title = (
    <>
      {formatQuota(t, terms.quota)}
      {terms.quotaSource.kind === "account" ? CUSTOM : null}
    </>
  );
  return { title, detail };
}

function formatQuota(t: Translate, quota: ResolvedRequestTerms["quota"]): string {
  if (quota.unlimited) return t("web.admin.users.detail.overview.access.unlimitedRequests");
  const period =
    quota.days === 1
      ? t("web.admin.users.detail.overview.access.quotaDay")
      : t("web.admin.users.detail.overview.access.quotaDays", {
          days: quota.days,
          days_text: String(quota.days),
        });
  return t("web.admin.users.detail.overview.access.quota", {
    count: quota.max,
    count_text: String(quota.max),
    period,
  });
}

const CUSTOM = (
  <span className="ms-1.5 inline-block align-middle">
    <SourceTag source="custom" />
  </span>
);

/** A few lines on what the account can do, with the account's own limits marked. */
export function AccessSummaryCard({ user }: { user: AdminUser }) {
  const t = useT();
  const groups = useAccessGroups().data ?? [];
  const libraries = useAdminLibraries().data ?? [];
  const capabilities = useAdminUserCapabilities().data;
  const accountDownloads = capabilities?.account_downloads === true;
  const downloads = useAdminUserDownloadSummary(user.id, accountDownloads);
  const groupName = groups.find((group) => group.id === user.access_group_id)?.name;
  const { terms, server } = useAccountRequestTerms(user, groupName);
  const effective = user.effective_policy;
  const ctx = inheritContextFor(user, groups);
  const custom = countCustomPolicyRows(user) + countCustomRequestTerms(terms);
  const source =
    ctx.kind === "group"
      ? t("web.admin.users.detail.overview.access.groupSource", { name: ctx.name })
      : t("web.admin.users.detail.overview.access.serverDefault");

  const marker = hasAssignedPermission(effective.permissions, PERMISSION_MARKER_EDIT);
  const curate = hasAssignedPermission(effective.permissions, PERMISSION_METADATA_CURATION);
  const videoText = videoTranscodingText(
    t,
    videoTranscodingFromEffective(effective.transcode_allowed, effective.max_transcodes),
  );
  const videoCustom = user.transcode_allowed !== null || user.max_transcodes !== null;
  const remote = effective.max_remote_stream_bitrate_kbps;

  const summary = downloads.data;
  const downloadsDetail = [
    effective.download_transcode_allowed
      ? t("web.admin.users.detail.overview.access.serverCopiesAllowed")
      : t("web.admin.users.detail.overview.access.originalFilesOnly"),
    ...(accountDownloads && summary
      ? [
          t("web.admin.users.detail.overview.access.downloadsConfirmed", {
            count: summary.completed,
            count_text: String(summary.completed),
          }),
          t("web.admin.users.detail.overview.access.seriesMonitored", {
            count: summary.monitored_series,
            count_text: String(summary.monitored_series),
          }),
        ]
      : []),
  ].join(" · ");

  const canRequest = effective.requests_allowed && server?.requests_enabled !== false;
  const requestsLine = requestsSummary(t, canRequest, terms);

  return (
    <DetailCard
      title={t("web.admin.users.detail.overview.access.title")}
      description={
        custom > 0
          ? t("web.admin.users.detail.overview.access.withLimits", { count: custom, source })
          : source
      }
      actions={
        <Button asChild variant="outline" size="xs">
          <Link to={userDetailTabSearch("access")}>
            {t("web.admin.users.detail.overview.access.openAccess")}
          </Link>
        </Button>
      }
    >
      <SummaryLine
        icon={<LayoutGrid />}
        title={libraryListText(effective.library_ids, libraries)}
        detail={t("web.admin.users.detail.overview.access.markersCurate", {
          marker: marker ? "yes" : "no",
          curate: curate ? "yes" : "no",
        })}
      />
      <SummaryLine
        icon={<Play />}
        title={
          <>
            {playbackQualityPresetFromValue(effective.max_playback_quality) === "any"
              ? t("web.admin.users.detail.overview.access.anyQuality")
              : t("web.admin.users.detail.overview.access.quality", {
                  quality: formatQuality(effective.max_playback_quality),
                })}
            {user.max_playback_quality !== null ? CUSTOM : null}
            {" · "}
            {effective.max_streams === 0
              ? t("web.admin.users.detail.overview.access.unlimitedStreams")
              : t("web.admin.users.detail.overview.access.streams", {
                  count: effective.max_streams,
                  count_text: String(effective.max_streams),
                })}
            {user.max_streams !== null ? CUSTOM : null}
          </>
        }
        detail={
          <>
            {videoText}
            {videoCustom ? CUSTOM : null}
            {" · "}
            {remote === 0
              ? t("web.admin.users.detail.overview.access.noBitrateCap")
              : t("web.admin.users.detail.overview.access.remoteCap", {
                  bitrate: formatStreamBitrateLimit(remote),
                })}
          </>
        }
      />
      <SummaryLine
        icon={<Download />}
        title={
          effective.download_allowed
            ? t("web.admin.users.detail.overview.access.downloadsAllowed")
            : t("web.admin.users.detail.overview.access.downloadsNotAllowed")
        }
        detail={downloadsDetail}
      />
      <SummaryLine icon={<Mail />} title={requestsLine.title} detail={requestsLine.detail} />
    </DetailCard>
  );
}
