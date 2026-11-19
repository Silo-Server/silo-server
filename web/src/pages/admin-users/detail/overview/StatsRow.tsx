import type { ReactNode } from "react";

import type { AdminSession, AdminUser } from "@/api/types";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminUserCapabilities } from "@/hooks/queries/admin/users";
import {
  useAdminUserProfileActivity,
  useAdminUserRequestUsage,
  useAdminUserWatchSummary,
} from "@/hooks/queries/admin/userActivity";
import { useT } from "@/i18n";
import { classifyActivityMethod } from "@/pages/adminActivityPresentation";

import { formatWatchTime } from "../format";
import { StatStrip, StatTile } from "../ui";

const LOADING = <Skeleton className="h-6 w-12" />;
const FAILED = "—";

function ratio(n: number, max: number) {
  return max > 0 ? n / max : 0;
}

/** A count's total and bar against a limit; a limit of 0 means none, so neither shows. */
function againstLimit(n: number, max: number) {
  return max > 0 ? { total: max, progress: n / max } : {};
}

/** Live sessions the server classifies as a video transcode. */
function countTranscodes(sessions: AdminSession[]): number {
  return sessions.filter((session) => classifyActivityMethod(session) === "transcode").length;
}

/**
 * The Overview's numbers: what the account uses now against its limits, and
 * how much it watched. A tile whose data this server can't report is left out.
 */
export function StatsRow({
  user,
  live,
}: {
  user: AdminUser;
  /** The account's live sessions; undefined while loading, null on error. */
  live: AdminSession[] | undefined | null;
}) {
  const t = useT();
  const capabilities = useAdminUserCapabilities().data;
  const requestUsage = capabilities?.request_usage === true;
  const watchSummary = capabilities?.watch_summary === true;
  const profiles = useAdminUserProfileActivity(user.id);
  const usage = useAdminUserRequestUsage(user.id, requestUsage);
  const watched = useAdminUserWatchSummary(user.id, { days: 30, enabled: watchSummary });
  const effective = user.effective_policy;

  const tiles: ReactNode[] = [];
  const liveMissing = live === undefined ? LOADING : FAILED;

  if (!live) {
    tiles.push(
      <StatTile
        key="streams"
        label={t("web.admin.users.detail.overview.stats.streamsNow")}
        value={liveMissing}
      />,
    );
  } else {
    tiles.push(
      <StatTile
        key="streams"
        label={t("web.admin.users.detail.overview.stats.streamsNow")}
        value={live.length}
        {...againstLimit(live.length, effective.max_streams)}
      />,
    );
  }

  if (!effective.transcode_allowed) {
    tiles.push(
      <StatTile
        key="transcodes"
        label={t("web.admin.users.detail.overview.stats.transcodesNow")}
        value={t("web.admin.users.detail.overview.stats.off")}
      />,
    );
  } else if (!live) {
    tiles.push(
      <StatTile
        key="transcodes"
        label={t("web.admin.users.detail.overview.stats.transcodesNow")}
        value={liveMissing}
      />,
    );
  } else {
    const n = countTranscodes(live);
    tiles.push(
      <StatTile
        key="transcodes"
        label={t("web.admin.users.detail.overview.stats.transcodesNow")}
        value={n}
        {...againstLimit(n, effective.max_transcodes)}
      />,
    );
  }

  const profileCount = profiles.data?.length;
  tiles.push(
    <StatTile
      key="profiles"
      label={t("web.admin.users.detail.overview.stats.profiles")}
      value={profiles.isError ? FAILED : profileCount === undefined ? LOADING : profileCount}
      total={profileCount === undefined ? undefined : effective.max_profiles}
      progress={
        profileCount === undefined ? undefined : ratio(profileCount, effective.max_profiles)
      }
    />,
  );

  if (requestUsage) {
    const data = usage.data;
    const label = data
      ? t("web.admin.users.detail.overview.stats.requestsWindow", {
          days: data.window_days,
          days_text: data.window_days,
        })
      : t("web.admin.users.detail.overview.stats.requests");
    if (usage.isError) {
      tiles.push(<StatTile key="requests" label={label} value={FAILED} />);
    } else if (!data) {
      tiles.push(<StatTile key="requests" label={label} value={LOADING} />);
    } else if (!data.requests_enabled || !data.allowed) {
      tiles.push(
        <StatTile
          key="requests"
          label={label}
          value={t("web.admin.users.detail.overview.stats.off")}
        />,
      );
    } else if (data.unlimited) {
      tiles.push(
        <StatTile
          key="requests"
          label={label}
          value={t("web.admin.users.detail.overview.stats.unlimited")}
        />,
      );
    } else {
      tiles.push(
        <StatTile
          key="requests"
          label={label}
          value={data.used}
          total={data.max_requests}
          progress={ratio(data.used, data.max_requests)}
        />,
      );
    }
  }

  if (watchSummary) {
    const data = watched.data;
    tiles.push(
      <StatTile
        key="watched"
        label={t("web.admin.users.detail.overview.stats.watched")}
        value={watched.isError ? FAILED : data ? formatWatchTime(data.watched_seconds) : LOADING}
        detail={
          data
            ? t("web.admin.users.detail.overview.stats.plays", {
                count: data.plays,
                count_text: data.plays,
              })
            : undefined
        }
      />,
    );
  }

  return <StatStrip columns={tiles.length >= 5 ? 5 : 4}>{tiles}</StatStrip>;
}
