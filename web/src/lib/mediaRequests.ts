import type {
  CreateMediaRequestInput,
  MediaRequest,
  MediaRequestOutcome,
  MediaRequestStatus,
  RequestMediaResult,
  RequestMediaSeason,
  RequestSeasonProgress,
  RequestMediaType,
  RequestSearchMediaType,
  RequestUserState,
} from "@/api/types";
import { t } from "@/i18n";
import { formatDate } from "@/lib/datetime";

export function formatMediaType(mediaType: RequestMediaType): string {
  return mediaType === "series" ? t("web.requests.toAdd.series") : t("web.requests.toAdd.movie");
}

export function formatRequestStatus(status?: MediaRequestStatus): string {
  switch (status) {
    case "pending":
      return t("web.requests.status.pending");
    case "approved":
      return t("web.requests.status.approved");
    case "queued":
      return t("web.requests.status.queued");
    case "downloading":
      return t("web.requests.status.downloading");
    case "completed":
      return t("web.requests.status.completed");
    default:
      return t("web.requests.status.requested");
  }
}

export function formatRequestOutcome(outcome?: MediaRequestOutcome): string {
  switch (outcome) {
    case "active":
      return t("web.requests.status.active");
    case "declined":
      return t("web.requests.status.declined");
    case "cancelled":
      return t("web.requests.status.cancelled");
    case "failed":
      return t("web.requests.status.failed");
    default:
      return t("web.requests.status.active");
  }
}

/**
 * The request states the user-facing request pages show. Admin views keep the
 * raw status and outcome. The server derives this state (v2 `state`), and its
 * value wins; the fallback for older servers collapses status and outcome the
 * same way: queued and downloading read as Processing, completed as
 * Available, and a closed outcome wins over the status it closed at.
 */
export type RequestDisplayState = RequestUserState;

export function requestDisplayState(
  status?: MediaRequestStatus,
  outcome?: MediaRequestOutcome,
  state?: RequestUserState,
): RequestDisplayState | undefined {
  if (state) return state;
  if (outcome === "declined" || outcome === "cancelled" || outcome === "failed") return outcome;
  switch (status) {
    case "pending":
    case "approved":
      return status;
    case "queued":
    case "downloading":
      return "processing";
    case "completed":
      return "available";
    default:
      return undefined;
  }
}

export function formatRequestDisplayState(state: RequestDisplayState): string {
  switch (state) {
    case "pending":
    case "approved":
      return formatRequestStatus(state);
    case "processing":
      return t("web.requests.status.processing");
    case "partially_available":
      return t("web.requests.status.partiallyAvailable");
    case "available":
      return t("web.requests.status.available");
    default:
      return formatRequestOutcome(state);
  }
}

/**
 * Mirrors the server's rule for withdrawing a request, which decline and
 * cancel share: a request can be withdrawn until something has been sent for
 * it, so while it is pending, or approved with no target yet. (The server also
 * refuses the moment a send is in flight.)
 */
export function canWithdrawRequest(
  request: Pick<MediaRequest, "status" | "outcome" | "targets">,
): boolean {
  if (request.outcome !== "active") return false;
  if (request.status === "pending") return true;
  return request.status === "approved" && (request.targets?.length ?? 0) === 0;
}

/**
 * Whether an owner can cancel their request: the withdrawal rule above.
 * Callers must already know the viewer owns the request.
 */
export function canCancelOwnRequest(
  request: Pick<MediaRequest, "status" | "outcome" | "targets">,
): boolean {
  return canWithdrawRequest(request);
}

/**
 * Mirrors the server's rule for removing a failed request: it failed and
 * delivered nothing. One that delivered a quality before failing stays for an
 * admin to retry. Callers must already know the viewer owns the request.
 */
export function canRemoveOwnRequest(request: Pick<MediaRequest, "outcome" | "targets">): boolean {
  return (
    request.outcome === "failed" &&
    !(request.targets ?? []).some((target) => target.status === "completed")
  );
}

/**
 * The detail page of a TMDB title. It serves titles outside the library; one
 * the viewer can open in the library redirects to its item page.
 */
export function requestDetailHref(mediaType: RequestMediaType, tmdbID: number): string {
  return `/title/${mediaType}/${tmdbID}`;
}

/** The media type a title URL names, or undefined for any other segment. */
export function parseRequestMediaType(value: string | undefined): RequestMediaType | undefined {
  return value === "movie" || value === "series" ? value : undefined;
}

/**
 * The app's search page, scoped to what can be requested: movies and series,
 * or just one of them. Its "Request to add" section lists the TMDB matches.
 */
export function requestSearchHref(query: string, mediaType?: string | null): string {
  const params = new URLSearchParams({
    source: "query",
    q: query.trim(),
    type: parseRequestMediaType(mediaType ?? undefined) ?? "video",
  });
  return `/catalog?${params.toString()}`;
}

/**
 * The TMDB search type for a catalog search scope, or null when the scope
 * holds nothing TMDB can supply (audiobooks, ebooks, manga).
 */
export function requestSearchTypeForScope(
  scope: string | undefined,
): RequestSearchMediaType | null {
  switch (scope) {
    case undefined:
    case "video":
      return "all";
    case "movie":
      return "movie";
    case "series":
    case "episode":
      return "series";
    default:
      return null;
  }
}

/** TMDB serves at most 500 pages of any list, whatever total it reports. */
export const TMDB_MAX_PAGE = 500;

/** The number of pages a TMDB list can actually be read to. */
export function tmdbPageCount(totalPages: number | undefined): number {
  return Math.min(Math.max(totalPages ?? 0, 0), TMDB_MAX_PAGE);
}

/** TMDB's page size, the placeholder count before any page has loaded. */
const TMDB_PAGE_SIZE = 20;

/**
 * How many placeholder cards stand in for the page being loaded: as many as
 * the last page held, so the grid barely moves when the titles arrive.
 */
export function pendingPageSize(
  pages: readonly { results: RequestMediaResult[] }[] | undefined,
): number {
  return pages?.at(-1)?.results.length || TMDB_PAGE_SIZE;
}

/**
 * The titles of a list read page by page, in order. A TMDB list can shift
 * between page reads (popularity changes), so a title already shown on an
 * earlier page is dropped.
 */
export function flattenResultPages(
  pages: readonly { results: RequestMediaResult[] }[] | undefined,
): RequestMediaResult[] {
  const seen = new Set<string>();
  const out: RequestMediaResult[] = [];
  for (const page of pages ?? []) {
    for (const item of page.results) {
      const key = `${item.media_type}-${item.tmdb_id}`;
      if (seen.has(key)) continue;
      seen.add(key);
      out.push(item);
    }
  }
  return out;
}

/** The page listing every title of a Discover row, such as Trending Movies. */
export function requestDiscoverSectionHref(sectionKey: string): string {
  return `/requests/discover/${encodeURIComponent(sectionKey)}`;
}

/** Request suggestions the ⌘K dialog lists below the library results. */
export const REQUEST_DIALOG_SUGGESTION_LIMIT = 4;

/** Search results worth suggesting as requests: titles not already in the library. */
export function requestSuggestions(
  results: RequestMediaResult[] | undefined,
  limit: number,
): RequestMediaResult[] {
  return (results ?? []).filter((item) => item.availability !== "available").slice(0, limit);
}

export function formatRequestReason(reason?: string): string {
  switch (reason) {
    case "already_requested":
      return t("web.requests.status.reason.alreadyRequested");
    case "already_available":
      return t("web.requests.status.reason.alreadyAvailable");
    case "requests_disabled":
      return t("web.requests.status.reason.requestsDisabled");
    case "blocked":
      return t("web.requests.status.reason.blocked");
    case "quota_exceeded":
      return t("web.requests.status.reason.quotaExceeded");
    default:
      return t("web.requests.status.reason.unavailable");
  }
}

/** Names requested seasons compactly: "Season 2", "Seasons 1–3, 5". */
export function formatSeasonList(seasons: number[]): string {
  const sorted = [...new Set(seasons)].sort((a, b) => a - b);
  const runs: string[] = [];
  for (let i = 0; i < sorted.length; ) {
    let j = i;
    while (j + 1 < sorted.length && sorted[j + 1] === sorted[j]! + 1) j++;
    runs.push(j > i ? `${sorted[i]}–${sorted[j]}` : String(sorted[i]));
    i = j + 1;
  }
  return t("web.requests.status.seasonList", { count: sorted.length, list: runs.join(", ") });
}

/**
 * A season has started airing, by TMDB's dates, compared with today's UTC
 * date as the server compares them.
 */
export function seasonHasAired(season: RequestMediaSeason, now = new Date()): boolean {
  return (
    season.episode_count > 0 &&
    Boolean(season.air_date) &&
    season.air_date! <= now.toISOString().slice(0, 10)
  );
}

/** A season a new request can still ask for: not complete in the library, not already asked for. */
export function seasonRequestable(season: RequestMediaSeason): boolean {
  return season.availability !== "available" && !season.requested;
}

/**
 * The seasons a series request asks for unless the viewer changes them: every
 * aired season not yet in the library, as the server picks when given none.
 */
export function defaultRequestSeasons(seasons: RequestMediaSeason[], now = new Date()): number[] {
  return seasons
    .filter((season) => seasonRequestable(season) && seasonHasAired(season, now))
    .map((season) => season.season_number);
}

/**
 * The series' latest regular season: the newest one that has aired, or the
 * first announced one when none has. Null when that season can't be requested.
 */
export function latestRequestSeason(
  seasons: RequestMediaSeason[],
  now = new Date(),
): number | null {
  const regular = seasons.filter((season) => season.season_number > 0);
  const aired = regular.filter((season) => seasonHasAired(season, now));
  const latest = aired.length
    ? aired.reduce((a, b) => (b.season_number > a.season_number ? b : a))
    : regular
        .filter((season) => season.air_date)
        .reduce<RequestMediaSeason | null>(
          (a, b) => (a === null || b.season_number < a.season_number ? b : a),
          null,
        );
  return latest && seasonRequestable(latest) ? latest.season_number : null;
}

/** The requestable regular seasons that haven't aired yet, announced or not. */
export function upcomingRequestSeasons(seasons: RequestMediaSeason[], now = new Date()): number[] {
  return seasons
    .filter(
      (season) =>
        season.season_number > 0 && seasonRequestable(season) && !seasonHasAired(season, now),
    )
    .map((season) => season.season_number);
}

/** "2022 · 9 episodes", or "Not announced" before TMDB dates or fills the season. */
export function formatRequestSeasonMeta(season: RequestMediaSeason): string {
  const parts: string[] = [];
  if (season.air_date) parts.push(season.air_date.slice(0, 4));
  if (season.episode_count > 0) {
    parts.push(
      t("web.requests.status.seasonEpisodes", {
        count: season.episode_count,
        count_text: season.episode_count,
      }),
    );
  }
  return parts.join(" · ") || t("web.requests.seasons.notAnnounced");
}

/**
 * "14 of 20 episodes in the library", counting the aired episodes of the
 * requested seasons; "" when none has aired by the library's dates.
 */
export function formatSeasonProgress(progress: RequestSeasonProgress[]): string {
  let aired = 0;
  let have = 0;
  for (const season of progress) {
    aired += season.episodes_aired;
    have += Math.min(season.episodes_available, season.episodes_aired);
  }
  if (aired === 0) return "";
  return t("web.requests.status.seasonProgress", {
    aired,
    have: have,
    aired_text: aired,
  });
}

export function formatRequestDate(request: Pick<MediaRequest, "created_at">): string {
  return formatDate(request.created_at, "medium");
}

export function tmdbImageURL(path?: string, size = "w342"): string | null {
  if (!path) return null;
  return `https://image.tmdb.org/t/p/${size}${path}`;
}

export function requestInputFromMediaResult(item: RequestMediaResult): CreateMediaRequestInput {
  return {
    media_type: item.media_type,
    tmdb_id: item.tmdb_id,
    title: item.title,
    year: item.year || undefined,
    overview: item.overview || undefined,
    poster_path: item.poster_path || undefined,
    backdrop_path: item.backdrop_path || undefined,
  };
}
