import type { WatchlistTitle } from "@/api/v2/watchlistTitles";
import { appLanguage } from "@/lib/appLocale";
import { t } from "@/i18n";
import { parseCalendarDate, preferredDateLocale } from "@/lib/datetime";
import {
  formatRequestDisplayState,
  formatRequestReason,
  requestDisplayState,
} from "@/lib/mediaRequests";
import { formatRequestDownload, requestDownloadPercent } from "@/lib/requestDownload";
import type { OverlayIconId } from "@/lib/overlays";

/**
 * How a watchlist title outside the library reads on its card: a short badge
 * for the poster overlay, and a caption that always carries the full status in
 * words, so the card stays clear with overlays turned off.
 */
export interface WatchlistTitleStatus {
  /** The request_status badge: "Downloading 43%", "Out Dec 18", "Awaiting approval". */
  badge: string;
  badgeIcon: OverlayIconId | null;
  /** The caption before any "Find it" link: "Out Dec 18 · awaiting approval". */
  caption: string;
  /** TMDB no longer has the title as saved; the viewer has to find it again. */
  attention: boolean;
  /** Download progress for the card's bar, while a download reports a size. */
  downloadPercent?: number;
  /** The title has no active request, and the viewer may request it now. */
  requestable: boolean;
  /** Sort group for "Soonest first"; lower sorts first. */
  rank: WatchlistTitleRank;
}

/**
 * Soonest first: downloading, then by release date, then awaiting approval,
 * then not requested, then needs attention.
 */
export const WATCHLIST_TITLE_RANK = {
  downloading: 0,
  dated: 1,
  awaitingApproval: 2,
  notRequested: 3,
  attention: 4,
} as const;
export type WatchlistTitleRank = (typeof WATCHLIST_TITLE_RANK)[keyof typeof WATCHLIST_TITLE_RANK];

function startOfDay(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth(), now.getDate());
}

/** "Dec 18", with the year when it isn't this year. */
function formatReleaseDay(date: Date, now: Date): string {
  return date.toLocaleDateString(preferredDateLocale(), {
    month: "short",
    day: "numeric",
    ...(date.getFullYear() !== now.getFullYear() ? { year: "numeric" } : {}),
  });
}

/** Whether TMDB lost the title as saved: needs_review or removed. */
export function watchlistTitleNeedsAttention(title: Pick<WatchlistTitle, "status">): boolean {
  return title.status === "needs_review" || title.status === "removed";
}

export function watchlistTitleStatus(
  title: WatchlistTitle,
  now: Date = new Date(),
): WatchlistTitleStatus {
  const request = title.request;
  if (title.status === "needs_review") {
    return {
      badge: t("web.library.watchlist.statusNeedsAttention"),
      badgeIcon: "alert",
      caption: t("web.library.watchlist.statusListedTwice"),
      attention: true,
      requestable: false,
      rank: WATCHLIST_TITLE_RANK.attention,
    };
  }
  if (title.status === "removed") {
    return {
      badge: t("web.library.watchlist.statusNotOnTmdb"),
      badgeIcon: "alert",
      caption: t("web.library.watchlist.statusNoLongerListed"),
      attention: true,
      requestable: false,
      rank: WATCHLIST_TITLE_RANK.attention,
    };
  }

  const state = requestDisplayState(request.status, undefined, request.state);
  const release = parseCalendarDate(title.release_date);
  const upcoming = release !== null && release.getTime() > startOfDay(now).getTime();
  const releaseDay = upcoming && release ? formatReleaseDay(release, now) : null;
  const out = releaseDay ? t("web.library.watchlist.statusOut", { date: releaseDay }) : null;

  if (request.download) {
    const percent = requestDownloadPercent(request.download);
    const downloading = request.download.phase === "downloading";
    return {
      badge:
        downloading && percent !== undefined
          ? t("web.library.watchlist.statusDownloading", { percent })
          : formatRequestDownload(request.download, { now }).split(" · ")[0]!,
      badgeIcon: "download",
      caption: formatRequestDownload(request.download, { now }),
      attention: false,
      downloadPercent: percent,
      requestable: false,
      rank: WATCHLIST_TITLE_RANK.downloading,
    };
  }

  switch (state) {
    case undefined:
    case "declined":
    case "cancelled":
    case "failed": {
      const refused = !request.requestable && request.reason ? request.reason : undefined;
      const lower = (text: string) => text.toLocaleLowerCase(appLanguage());
      return {
        badge: t("web.library.watchlist.statusNotRequested"),
        badgeIcon: null,
        caption: refused
          ? t("web.library.watchlist.statusNotRequestedReason", {
              reason: lower(formatRequestReason(refused)),
            })
          : releaseDay
            ? t("web.library.watchlist.statusNotRequestedOut", { date: lower(releaseDay) })
            : t("web.library.watchlist.statusNotRequested"),
        attention: false,
        requestable: request.requestable,
        rank: WATCHLIST_TITLE_RANK.notRequested,
      };
    }
    case "processing":
      return {
        badge: t("web.requests.status.processing"),
        badgeIcon: "download",
        caption: t("web.requests.status.processing"),
        attention: false,
        requestable: false,
        rank: WATCHLIST_TITLE_RANK.downloading,
      };
    case "pending":
      return {
        badge: out ?? t("web.library.watchlist.statusAwaitingApproval"),
        badgeIcon: out ? "calendar" : "clock",
        caption: releaseDay
          ? t("web.library.watchlist.statusOutAwaitingApproval", { date: releaseDay })
          : t("web.library.watchlist.statusAwaitingApproval"),
        attention: false,
        requestable: false,
        rank: out ? WATCHLIST_TITLE_RANK.dated : WATCHLIST_TITLE_RANK.awaitingApproval,
      };
    case "approved":
      return {
        badge: out ?? t("web.requests.status.approved"),
        badgeIcon: out ? "calendar" : "hourglass",
        caption: releaseDay
          ? t("web.library.watchlist.statusOutApproved", { date: releaseDay })
          : t("web.library.watchlist.statusApprovedWaiting"),
        attention: false,
        requestable: false,
        rank: WATCHLIST_TITLE_RANK.dated,
      };
    default: {
      const label = formatRequestDisplayState(state);
      return {
        badge: label,
        badgeIcon: null,
        caption: label,
        attention: false,
        requestable: false,
        rank: WATCHLIST_TITLE_RANK.dated,
      };
    }
  }
}

function releaseTime(title: WatchlistTitle): number {
  return parseCalendarDate(title.release_date)?.getTime() ?? Number.POSITIVE_INFINITY;
}

function addedTime(title: WatchlistTitle): number {
  const t = Date.parse(title.added_at);
  return Number.isFinite(t) ? t : 0;
}

/**
 * "Soonest first": downloading, then by release date, then awaiting approval,
 * then not requested, then needs attention. Within a group, the earlier
 * release date first (unknown dates last), then the most recently added.
 */
export function sortWatchlistTitlesSoonestFirst(
  titles: readonly WatchlistTitle[],
  now: Date = new Date(),
): WatchlistTitle[] {
  const ranked = titles.map((title) => ({ title, rank: watchlistTitleStatus(title, now).rank }));
  ranked.sort((a, b) => {
    if (a.rank !== b.rank) return a.rank - b.rank;
    const releaseA = releaseTime(a.title);
    const releaseB = releaseTime(b.title);
    if (releaseA !== releaseB) return releaseA < releaseB ? -1 : 1;
    return addedTime(b.title) - addedTime(a.title);
  });
  return ranked.map((entry) => entry.title);
}

/** The `?tab=` value of the watchlist's "Not in your library yet" tab. */
export const WATCHLIST_NOT_IN_LIBRARY_TAB = "not-in-library";

export type WatchlistTab = "library" | typeof WATCHLIST_NOT_IN_LIBRARY_TAB;

/** The watchlist tab a `?tab=` value names; anything else is the library tab. */
export function parseWatchlistTab(value: string | null): WatchlistTab {
  return value === WATCHLIST_NOT_IN_LIBRARY_TAB ? WATCHLIST_NOT_IN_LIBRARY_TAB : "library";
}

/**
 * The hint at the top of the titles tab. The request and notification clauses
 * hold only while adds request: the notification comes from the request.
 */
export function watchlistTitlesHint(watchlistRequests: boolean): string {
  return t(
    watchlistRequests
      ? "web.library.watchlist.titlesHintRequested"
      : "web.library.watchlist.titlesHint",
  );
}

/**
 * Whether the watchlist can hold titles the library doesn't have. The titles
 * belong to the requests surface: with requests off their operations answer
 * 409 capability_disabled, so both flags must hold.
 */
export function watchlistTitlesAvailable(
  status: { requests_enabled?: boolean; watchlist_titles_supported?: boolean } | undefined,
): boolean {
  return status?.requests_enabled === true && status.watchlist_titles_supported === true;
}

/**
 * Whether the profile's watchlist auto-request switch applies. The request
 * status reports the effective answer (server setting, this profile's opt-in,
 * and whether the viewer may request), not the server setting on its own. A
 * profile that opted out still sees the switch so it can opt back in; with its
 * opt-in on and the effective answer off, the server setting is off.
 */
export function showWatchlistAutoRequestControl(
  status:
    | {
        requests_enabled?: boolean;
        allowed?: boolean;
        watchlist_titles_supported?: boolean;
        watchlist_requests?: boolean;
      }
    | undefined,
  profileValue: boolean,
): boolean {
  if (!watchlistTitlesAvailable(status) || !status?.allowed) return false;
  return status.watchlist_requests === true || profileValue === false;
}
