import type { WatchlistTitle } from "@/api/v2/watchlistTitles";
import { preferredDateLocale } from "@/lib/datetime";
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

/** A YYYY-MM-DD calendar date as local midnight, or null when unparseable. */
function calendarDate(value: string | undefined): Date | null {
  const match = value ? /^(\d{4})-(\d{2})-(\d{2})$/.exec(value) : null;
  if (!match) return null;
  const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
  return Number.isNaN(date.getTime()) ? null : date;
}

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
      badge: "Needs attention",
      badgeIcon: "alert",
      caption: "TMDB lists it twice",
      attention: true,
      requestable: false,
      rank: WATCHLIST_TITLE_RANK.attention,
    };
  }
  if (title.status === "removed") {
    return {
      badge: "Not on TMDB",
      badgeIcon: "alert",
      caption: "No longer listed on TMDB",
      attention: true,
      requestable: false,
      rank: WATCHLIST_TITLE_RANK.attention,
    };
  }

  const state = requestDisplayState(request.status, undefined, request.state);
  const release = calendarDate(title.release_date);
  const upcoming = release !== null && release.getTime() > startOfDay(now).getTime();
  const out = upcoming && release ? `Out ${formatReleaseDay(release, now)}` : null;

  if (request.download) {
    const percent = requestDownloadPercent(request.download);
    const downloading = request.download.phase === "downloading";
    return {
      badge:
        downloading && percent !== undefined
          ? `Downloading ${percent}%`
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
      const detail = refused ? formatRequestReason(refused).toLowerCase() : null;
      return {
        badge: "Not requested",
        badgeIcon: null,
        caption: ["Not requested", detail ?? (out ? out.toLowerCase() : null)]
          .filter(Boolean)
          .join(" · "),
        attention: false,
        requestable: request.requestable,
        rank: WATCHLIST_TITLE_RANK.notRequested,
      };
    }
    case "processing":
      return {
        badge: "Processing",
        badgeIcon: "download",
        caption: "Processing",
        attention: false,
        requestable: false,
        rank: WATCHLIST_TITLE_RANK.downloading,
      };
    case "pending":
      return {
        badge: out ?? "Awaiting approval",
        badgeIcon: out ? "calendar" : "clock",
        caption: out ? `${out} · awaiting approval` : "Awaiting approval",
        attention: false,
        requestable: false,
        rank: out ? WATCHLIST_TITLE_RANK.dated : WATCHLIST_TITLE_RANK.awaitingApproval,
      };
    case "approved":
      return {
        badge: out ?? "Approved",
        badgeIcon: out ? "calendar" : "hourglass",
        caption: out ? `${out} · approved` : "Approved · waiting for a download",
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
  return calendarDate(title.release_date)?.getTime() ?? Number.POSITIVE_INFINITY;
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
  return [
    "These titles aren't in the library yet, so they can't be played.",
    watchlistRequests
      ? "They've been requested for you. When one arrives, it moves to In your library and you get a notification."
      : "When one arrives, it moves to In your library.",
  ].join(" ");
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
