/**
 * The sliding-window schedule shared by the subtitle fetchers
 * (useSubtitleTracks for text, usePGSSubtitles for bitmaps). An embedded track
 * can take minutes to extract whole from a large remux, while a positioned
 * window comes back in a second or two, so each fetcher asks the server for
 * bounded source-time windows around the playhead.
 */

import { toMediaTime } from "./mediaTimeline";
import { isSubtitleSourceChanged } from "./subtitleSourceChanged";

// The source-time range a bounded subtitle response produced. It is `<start>-<end>`
// in seconds with three decimals, or `<start>-*` when the extract runs to the end
// of the input. A page the server answers from its progressive artifact before
// the artifact has parsed the page's end carries the shorter range it really
// produced.
export const SUBTITLE_COVERAGE_HEADER = "X-Subtitle-Coverage";
// Explicitly bound each subtitle fetch to this many source-time seconds. A
// bitmap slice has to be fully extracted before it can be painted, so a cold
// remote 600 s window costs ~18-24 s. Two minutes keeps the first paint short;
// the prefetch lead below leaves time to fetch the next slice.
export const SUBTITLE_WINDOW_SECONDS = 120;
// One bitmap page. A bitmap request asks for a whole, grid-aligned page so the
// server stores exactly what the next request asks for and every later request
// inside the page is served from it. This must equal the page span the server
// aligns bitmap windows to (internal/api/handlers/stream.go,
// virtualSubtitleBitmapImplicitWindowSeconds); the two sides floor the same
// playhead onto the same grid, and a mismatch is exactly what left every page
// unreusable.
export const BITMAP_SUBTITLE_PAGE_SECONDS = 120;
// Start fetching the next window this many seconds before the current one's
// requested end, so the new cues are already on hand when playback crosses in.
export const SUBTITLE_PREFETCH_LEAD_SECONDS = 30;
// Overlap consecutive windows by a few seconds so ffmpeg boundary rounding
// can't drop a cue that straddles the join.
export const SUBTITLE_WINDOW_OVERLAP_SECONDS = 5;
// Abort a window fetch when the response goes this long without delivering a
// chunk. Extraction streams progressively, so a healthy-but-slow ffmpeg keeps
// resetting the clock; only a genuinely hung one trips it.
export const SUBTITLE_FETCH_STALL_TIMEOUT_MS = 30_000;
// Wait this long after a failed window fetch before retrying, so a
// persistently failing extraction doesn't turn timeupdate into a fetch storm.
export const SUBTITLE_FETCH_RETRY_BACKOFF_MS = 5_000;
export const SUBTITLE_FETCH_RETRY_MAX_BACKOFF_MS = 60_000;
// Give up after this many consecutive failures that retrying cannot fix (a
// 404, a 415, a malformed stream). Retrying the same URL only burns requests.
export const SUBTITLE_FETCH_MAX_CONSECUTIVE_FAILURES = 4;
// Transient failures are worth more attempts, but still bounded: a server that
// keeps answering 503 must not keep a timeupdate loop fetching forever.
export const SUBTITLE_FETCH_MAX_RETRYABLE_FAILURES = 12;
// Statuses that describe a condition the same request may survive: a
// timeout, a rate limit, or a gateway that could not reach the extractor.
const RETRYABLE_WINDOW_STATUSES = new Set([408, 429, 502, 503, 504]);

export interface SubtitleWindowState {
  hasFetched: boolean;
  /** Source time the current coverage starts at. */
  coverageStart: number;
  /** Requested end of the last window that streamed in completely. */
  windowEnd: number;
  /** The last window reached the known media duration. */
  atEOF: boolean;
  /** Start of the window on the wire, or null when none is. */
  inflightStart: number | null;
  /**
   * Requested end of the window on the wire, or null when none is. Windows
   * differ in length (a short first slice, a longer extend), so the in-flight
   * coverage cannot be derived from a single window length.
   */
  inflightEnd?: number | null;
  /** Wall time of the last failed fetch, or 0. */
  failedAt: number;
  retryDelayMs: number;
}

export type SubtitleWindowAction =
  | { kind: "none" }
  /** Drop what is loaded and fetch from `start`. */
  | { kind: "fresh"; start: number }
  /** Append the window starting at `start` to the current coverage. */
  | { kind: "extend"; start: number };

/**
 * Picks the fetch for the current playhead:
 *   - no cues yet, or the playhead left the covered range (a seek) → a fresh
 *     window from slightly before the playhead, so a skipped-over gap is never
 *     mistaken for covered range;
 *   - playback nearing the end of coverage and not at EOF → the next window,
 *     overlapping the previous one slightly. It starts there even when the
 *     playhead has already passed the end (a retry after a failed extend), so
 *     nothing between the two windows is skipped;
 *   - a window already on the wire that covers the playhead, or a failure
 *     still inside its backoff → nothing.
 */
export function planSubtitleWindow(
  state: SubtitleWindowState,
  time: number,
  now: number,
  seekBackoffSeconds: number,
  windowSeconds = SUBTITLE_WINDOW_SECONDS,
  prefetchLeadSeconds = SUBTITLE_PREFETCH_LEAD_SECONDS,
): SubtitleWindowAction {
  const fresh = (): SubtitleWindowAction => ({
    kind: "fresh",
    start: Math.max(0, time - seekBackoffSeconds),
  });
  if (state.inflightStart !== null) {
    const inflightEnd = state.inflightEnd ?? state.inflightStart + windowSeconds;
    const covered =
      time >= Math.min(state.coverageStart, state.inflightStart) - 1 && time <= inflightEnd + 1;
    // A seek outside the requested range must not wait for extraction.
    return covered ? { kind: "none" } : fresh();
  }
  if (state.failedAt > 0 && now - state.failedAt < state.retryDelayMs) return { kind: "none" };
  if (!state.hasFetched || time < state.coverageStart - 1 || time > state.windowEnd + 1) {
    return fresh();
  }
  if (!state.atEOF && time > state.windowEnd - prefetchLeadSeconds) {
    return { kind: "extend", start: state.windowEnd - SUBTITLE_WINDOW_OVERLAP_SECONDS };
  }
  return { kind: "none" };
}

/** Backoff after another failed fetch: doubling, capped. */
export function nextSubtitleRetryDelay(previousMs: number): number {
  return Math.min(
    previousMs ? previousMs * 2 : SUBTITLE_FETCH_RETRY_BACKOFF_MS,
    SUBTITLE_FETCH_RETRY_MAX_BACKOFF_MS,
  );
}

/**
 * The minimum delay a `Retry-After` header asks for, in milliseconds. The
 * header carries either delta-seconds or an HTTP date; anything else (or a
 * date already past) means no extra delay.
 */
export function subtitleRetryAfterMs(header: string | null, now = Date.now()): number {
  if (!header) return 0;
  const value = header.trim();
  if (/^\d+$/.test(value)) return Number(value) * 1_000;
  const date = Date.parse(value);
  return Number.isFinite(date) ? Math.max(0, date - now) : 0;
}

/**
 * The source-time end a coverage header reports, or null when the response
 * carried none (a whole-track or unwindowed response) or an open end. The value
 * is what the response really produced, which for a bitmap page answered from a
 * partially parsed track is earlier than the page it asked for.
 */
export function subtitleCoverageEnd(header: string | null): number | null {
  if (!header) return null;
  const dash = header.indexOf("-");
  if (dash < 0) return null;
  const end = header.slice(dash + 1).trim();
  if (end === "*" || end === "") return null;
  const value = Number(end);
  return Number.isFinite(value) ? value : null;
}

/** The request for the window starting at `position`, in source seconds. */
export function subtitleWindowUrl(
  url: string,
  position: number,
  extraQuery = "",
  windowSeconds = SUBTITLE_WINDOW_SECONDS,
): string {
  const separator = url.includes("?") ? "&" : "?";
  const extra = extraQuery ? `${extraQuery}&` : "";
  return `${url}${separator}${extra}position=${position}&duration=${windowSeconds}`;
}

/**
 * Snap a source position down onto a window grid. Every position inside one
 * grid cell then asks for the same window start, so the server can answer it
 * from the window it already produced instead of demuxing a range shifted by
 * the playhead. `backoffSeconds` pulls the anchor back first so a small scrub
 * back stays inside the window; bitmap pages pass 0, because the page already
 * starts at or before the playhead.
 */
export function alignSubtitleWindowStart(
  position: number,
  cellSeconds: number,
  backoffSeconds = 0,
): number {
  if (!(cellSeconds > 0)) return Math.max(0, position);
  const anchored = Math.max(0, position - backoffSeconds);
  return Math.floor(anchored / cellSeconds) * cellSeconds;
}

/**
 * Source time at the playhead. Until the element has media loaded, currentTime
 * reads 0 rather than where playback will start (a resume target, or a seek
 * that restarted the stream), so the intended position stands in for it.
 */
export function subtitlePlayheadSeconds(
  video: HTMLVideoElement,
  streamOriginSeconds: number,
  fetchAnchorSeconds: number | null | undefined,
): number {
  return video.readyState > 0
    ? toMediaTime(video.currentTime, streamOriginSeconds)
    : (fetchAnchorSeconds ?? 0);
}

export interface SubtitleWindowCoverage {
  hasFetched: boolean;
  coverageStart: number;
  windowEnd: number;
  atEOF: boolean;
}

export interface SubtitleWindowFetcherOptions {
  /** Request URL for the window starting at `start` and lasting `duration` seconds. */
  windowUrl: (start: number, duration: number) => string;
  /** Source time at the playhead. */
  playhead: () => number;
  /** Known media duration in source seconds (0 when unknown). */
  duration: () => number;
  seekBackoffSeconds: number;
  /** Source-time seconds per window; defaults to SUBTITLE_WINDOW_SECONDS. */
  windowSeconds?: number;
  /**
   * Source-time seconds for a fresh window (initial fetch, seek, or track
   * change); defaults to `windowSeconds`. A short first slice keeps first
   * paint quick, while extends fetch the longer `windowSeconds` span.
   */
  firstWindowSeconds?: number;
  /** Seconds before the window end to schedule the next slice. */
  prefetchLeadSeconds?: number;
  /**
   * Fetch whole, grid-aligned pages of this many source seconds instead of a
   * playhead-anchored slice. A bitmap track uses the server's page span
   * (BITMAP_SUBTITLE_PAGE_SECONDS): every request is exactly the page the
   * server can store once and serve to every later request inside it, so the
   * prefetched page is by construction the next one the client asks for.
   * Unset keeps the sliding-window behavior that text uses.
   */
  pageSpanSeconds?: number;
  /** Current playback rate; media-time leads scale with it. */
  playbackRate?: () => number;
  /** Coverage carried over from an earlier fetcher for the same track. */
  initial?: SubtitleWindowCoverage;
  /**
   * A window is about to stream; `fresh` replaces everything loaded and is
   * shorter than an extend when `firstWindowSeconds` is set.
   */
  onWindowStart: (fresh: boolean) => void;
  /** The next bytes of the current window, in order. */
  onChunk: (chunk: Uint8Array) => void;
  /** The current window's response ended without an error. */
  onResponseEnd?: () => void;
  /** The whole window streamed in and its range now counts as covered. */
  onWindowCommitted: (fresh: boolean) => void;
  /** A window failed or stalled; it is retried after a backoff. */
  onWindowFailed: () => void;
  /**
   * The server answered `subtitle_source_changed` (409): the virtual release
   * rotated under this plan. No retry is scheduled and no failure budget is
   * spent; the caller's refreshed effect rebuilds the fetcher.
   */
  onSourceChanged?: () => void;
  /**
   * A failure budget ran out. No further window is fetched until the caller
   * rebuilds the fetcher.
   */
  onTerminal?: () => void;
  /** Abort a window that delivers nothing for this long; defaults to 30 s. */
  stallTimeoutMs?: number;
  /** Consecutive non-retryable failures before giving up; defaults to 4. */
  maxConsecutiveFailures?: number;
  /** Consecutive retryable failures before giving up; defaults to 12. */
  maxRetryableFailures?: number;
  logTag: string;
}

export interface SubtitleWindowFetcher {
  /**
   * Fetches whatever the playhead needs next; see planSubtitleWindow.
   * `seeked` marks a deliberate seek, which may revive a terminal fetcher.
   */
  maybeFetch: (seeked?: boolean) => void;
  coverage: () => SubtitleWindowCoverage;
  /** Drops coverage before `start` once the caller has released it. */
  releaseBefore: (start: number) => void;
  dispose: () => void;
}

/**
 * Runs the sliding-window fetch loop for one subtitle track: one window on
 * the wire at a time, a stall timeout re-armed on every read, coverage
 * committed only once a window has streamed in whole, and a backoff after a
 * failure. A `subtitle_source_changed` conflict ends this fetcher instead of
 * retrying it, and a bounded failure budget ends it instead of looping. The
 * caller parses the bytes.
 */
export function createSubtitleWindowFetcher(
  options: SubtitleWindowFetcherOptions,
): SubtitleWindowFetcher {
  let disposed = false;
  let hasFetched = options.initial?.hasFetched ?? false;
  let coverageStart = options.initial?.coverageStart ?? 0;
  let windowEnd = options.initial?.windowEnd ?? 0;
  let atEOF = options.initial?.atEOF ?? false;
  let inflight: AbortController | null = null;
  let inflightStart = 0;
  let inflightEnd: number | null = null;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let lastFetchFailureAt = 0;
  let retryDelay = 0;
  // Consecutive failures; a window that streams in whole resets both. The
  // budgets are separate because a timeout or overloaded gateway is worth
  // retrying far longer than a 404 the same request can never survive.
  let hardFailures = 0;
  let retryableFailures = 0;
  let terminal = false;
  let sourceChanged = false;
  let sourceChangedNotified = false;
  const windowSeconds = options.windowSeconds ?? SUBTITLE_WINDOW_SECONDS;
  const firstWindowSeconds = options.firstWindowSeconds ?? windowSeconds;
  const pageSpanSeconds = options.pageSpanSeconds ?? 0;
  const stallTimeoutMs = options.stallTimeoutMs ?? SUBTITLE_FETCH_STALL_TIMEOUT_MS;
  const maxConsecutiveFailures =
    options.maxConsecutiveFailures ?? SUBTITLE_FETCH_MAX_CONSECUTIVE_FAILURES;
  const maxRetryableFailures =
    options.maxRetryableFailures ?? SUBTITLE_FETCH_MAX_RETRYABLE_FAILURES;

  async function fetchWindow(seekStart: number, fresh: boolean) {
    // Only one window on the wire at a time: each fetch keeps an ffmpeg
    // process alive, so double-fetching wastes it.
    inflight?.abort();
    const controller = new AbortController();
    inflight = controller;
    inflightStart = seekStart;
    if (retryTimer !== null) {
      clearTimeout(retryTimer);
      retryTimer = null;
    }
    if (fresh) {
      coverageStart = seekStart;
      windowEnd = seekStart;
      atEOF = false;
    }
    options.onWindowStart(fresh);
    // A fresh window only has to cover first paint; an extend has to reach
    // far enough that the next one is due after the prefetch lead. A page grid
    // always asks for the whole page: a partial page is a range no later
    // request computes a key for, so it would never be reused.
    const duration =
      pageSpanSeconds > 0 ? pageSpanSeconds : fresh ? firstWindowSeconds : windowSeconds;
    const requestedEnd = seekStart + duration;
    inflightEnd = requestedEnd;

    // Abort the fetch if the response stops delivering chunks. Re-armed on
    // every read so a slow-but-progressing extraction is never cut off.
    let stallTimer: ReturnType<typeof setTimeout> | null = null;
    let stalled = false;
    const armStallTimer = () => {
      if (stallTimer !== null) clearTimeout(stallTimer);
      stallTimer = setTimeout(() => {
        stalled = true;
        controller.abort();
      }, stallTimeoutMs);
    };

    const url = options.windowUrl(seekStart, duration);
    let succeeded = false;
    let sourceChangedNow = false;
    let producedEnd = requestedEnd;
    let failure: { retryable: boolean; retryAfterMs: number } | null = null;
    try {
      armStallTimer();
      const response = await fetch(url, { signal: controller.signal });
      // A windowed .sup page can be answered before its own end: the server's
      // progressive artifact has parsed only part of it, so the response covers
      // less than it asked for and says so. Committing the requested end would
      // leave the rest of the page marked as covered and never requested again.
      const coveredEnd = subtitleCoverageEnd(
        response.headers?.get?.(SUBTITLE_COVERAGE_HEADER) ?? null,
      );
      if (coveredEnd !== null) {
        producedEnd = Math.min(Math.max(coveredEnd, seekStart), requestedEnd);
      }
      const sourceChangedResponse = await isSubtitleSourceChanged(response);
      // The 409 check reads the body asynchronously. A seek or disposal while
      // that read is pending must not signal a refresh for a request that is
      // no longer on the wire.
      if (disposed || inflight !== controller) return;
      if (sourceChangedResponse) {
        // The plan's subtitle inventory is stale, so this URL can never
        // succeed. Signal once; the caller refreshes the plan and rebuilds
        // this fetcher with a new URL. No retry, no failure budget.
        sourceChangedNow = true;
        sourceChanged = true;
        if (!sourceChangedNotified) {
          sourceChangedNotified = true;
          options.onSourceChanged?.();
        }
        return;
      }
      if (!response.ok || !response.body) {
        console.error(`[${options.logTag}] Failed to fetch ${url}: ${response.status}`);
        failure = {
          retryable: response.ok ? true : RETRYABLE_WINDOW_STATUSES.has(response.status),
          retryAfterMs: subtitleRetryAfterMs(response.headers?.get?.("Retry-After") ?? null),
        };
        return;
      }
      const reader = response.body.getReader();
      while (!disposed) {
        armStallTimer();
        const { value, done } = await reader.read();
        if (disposed || controller.signal.aborted || inflight !== controller) return;
        if (done) break;
        options.onChunk(value);
      }
      if (disposed) return;
      options.onResponseEnd?.();
      succeeded = true;
    } catch (error) {
      if ((error as Error).name !== "AbortError") {
        console.error(`[${options.logTag}] Stream error:`, error);
        failure = { retryable: true, retryAfterMs: 0 };
      } else if (stalled) {
        // Logged once the finally block knows this was not superseded.
        failure = { retryable: true, retryAfterMs: 0 };
      }
    } finally {
      if (stallTimer !== null) clearTimeout(stallTimer);
      const superseded = inflight !== controller;
      if (inflight === controller) {
        inflight = null;
        inflightEnd = null;
      }
      if (succeeded && !disposed && !superseded) {
        hasFetched = true;
        retryDelay = 0;
        lastFetchFailureAt = 0;
        hardFailures = 0;
        retryableFailures = 0;
        // Commit coverage only after the whole window streamed in, and only the
        // range the response says it produced: a page answered from a partially
        // parsed artifact is shorter than requested, and its tail must stay
        // uncovered so the next request asks for it again. A failed or stalled
        // fetch must leave the range uncovered too, or the gap would read as
        // fetched and never be retried.
        windowEnd = Math.max(windowEnd, producedEnd);
        // End-of-input only when the window reaches the known media duration.
        // Where a window's cues stop says nothing: a window that ends inside a
        // dialogue gap looks the same as the end of the file.
        const mediaDuration = options.duration();
        if (mediaDuration > 0 && producedEnd >= mediaDuration) atEOF = true;
        options.onWindowCommitted(fresh);
      } else if (!succeeded && !superseded && !disposed && !sourceChangedNow) {
        // A real failure (error, stall, or non-ok response) rather than a seek
        // superseding this fetch: count it, then back off or give up.
        if (stalled) {
          console.error(
            `[${options.logTag}] Window fetch stalled for ${stallTimeoutMs} ms; aborting ${url}`,
          );
        }
        const { retryable, retryAfterMs } = failure ?? {
          retryable: stalled,
          retryAfterMs: 0,
        };
        lastFetchFailureAt = Date.now();
        options.onWindowFailed();
        if (retryable) retryableFailures += 1;
        else hardFailures += 1;
        if (hardFailures >= maxConsecutiveFailures || retryableFailures >= maxRetryableFailures) {
          terminal = true;
          console.error(
            `[${options.logTag}] Giving up on subtitle windows after ${hardFailures} hard and ${retryableFailures} retryable consecutive failures`,
          );
          options.onTerminal?.();
        } else {
          // `Retry-After` raises the delay; it never shortens the backoff.
          retryDelay = Math.max(nextSubtitleRetryDelay(retryDelay), retryAfterMs);
          retryTimer = setTimeout(maybeFetch, retryDelay);
        }
      }
    }
  }

  function maybeFetch(seeked = false) {
    if (disposed || sourceChanged) return;
    if (terminal) {
      // A deliberate seek is a new window request, not a retry of the failed
      // one. Reset the exhausted budget so the viewer can recover without
      // rebuilding the whole track.
      if (!seeked) return;
      terminal = false;
      hardFailures = 0;
      retryableFailures = 0;
      retryDelay = 0;
      lastFetchFailureAt = 0;
    }
    let action = planSubtitleWindow(
      {
        hasFetched,
        coverageStart,
        windowEnd,
        atEOF,
        inflightStart: inflight ? inflightStart : null,
        inflightEnd: inflight ? inflightEnd : null,
        failedAt: lastFetchFailureAt,
        retryDelayMs: retryDelay,
      },
      options.playhead(),
      Date.now(),
      options.seekBackoffSeconds,
      windowSeconds,
      options.prefetchLeadSeconds == null
        ? undefined
        : options.prefetchLeadSeconds * Math.max(1, options.playbackRate?.() ?? 1),
    );
    if (pageSpanSeconds > 0) {
      // Page grid: a fresh window is the page that contains the playhead, and
      // an extend is the page that contains the coverage already committed. The
      // planner's own anchors (playhead minus a backoff, coverage minus an
      // overlap) are deliberately off that grid, so aligning them here is what
      // makes the request, the page the server stores, and the server's page
      // prefetch the same window. A page the server could only partly produce —
      // a page whose artifact has not parsed its end yet — commits the shorter
      // range it really covered, so the alignment asks for that same page again
      // rather than skipping the part it did not send.
      action =
        action.kind === "fresh"
          ? { kind: "fresh", start: alignSubtitleWindowStart(action.start, pageSpanSeconds) }
          : action.kind === "extend"
            ? { kind: "extend", start: alignSubtitleWindowStart(windowEnd, pageSpanSeconds) }
            : action;
    }
    if (action.kind === "fresh") void fetchWindow(action.start, true);
    else if (action.kind === "extend") void fetchWindow(action.start, false);
  }

  return {
    maybeFetch,
    coverage: () => ({ hasFetched, coverageStart, windowEnd, atEOF }),
    releaseBefore: (start) => {
      if (start > coverageStart) coverageStart = start;
    },
    dispose: () => {
      disposed = true;
      if (retryTimer !== null) clearTimeout(retryTimer);
      inflight?.abort();
      inflight = null;
    },
  };
}
