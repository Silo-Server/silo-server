/**
 * The sliding-window schedule shared by the subtitle fetchers
 * (useSubtitleTracks for text, usePGSSubtitles for bitmaps). An embedded track
 * can take minutes to extract whole from a large remux, while a positioned
 * window comes back in a second or two, so each fetcher asks the server for
 * bounded source-time windows around the playhead.
 */

import { toMediaTime } from "./mediaTimeline";

// Explicitly bound each subtitle fetch to this many source-time seconds.
export const SUBTITLE_WINDOW_SECONDS = 600;
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
): SubtitleWindowAction {
  const fresh = (): SubtitleWindowAction => ({
    kind: "fresh",
    start: Math.max(0, time - seekBackoffSeconds),
  });
  if (state.inflightStart !== null) {
    const covered =
      time >= Math.min(state.coverageStart, state.inflightStart) - 1 &&
      time <= state.inflightStart + SUBTITLE_WINDOW_SECONDS + 1;
    // A seek outside the requested range must not wait for extraction.
    return covered ? { kind: "none" } : fresh();
  }
  if (state.failedAt > 0 && now - state.failedAt < state.retryDelayMs) return { kind: "none" };
  if (!state.hasFetched || time < state.coverageStart - 1 || time > state.windowEnd + 1) {
    return fresh();
  }
  if (!state.atEOF && time > state.windowEnd - SUBTITLE_PREFETCH_LEAD_SECONDS) {
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

/** The request for the window starting at `position`, in source seconds. */
export function subtitleWindowUrl(url: string, position: number, extraQuery = ""): string {
  const separator = url.includes("?") ? "&" : "?";
  const extra = extraQuery ? `${extraQuery}&` : "";
  return `${url}${separator}${extra}position=${position}&duration=${SUBTITLE_WINDOW_SECONDS}`;
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
  /** Request URL for the window starting at `start`. */
  windowUrl: (start: number) => string;
  /** Source time at the playhead. */
  playhead: () => number;
  /** Known media duration in source seconds (0 when unknown). */
  duration: () => number;
  seekBackoffSeconds: number;
  /** Coverage carried over from an earlier fetcher for the same track. */
  initial?: SubtitleWindowCoverage;
  /** A window is about to stream; `fresh` replaces everything loaded. */
  onWindowStart: (fresh: boolean) => void;
  /** The next bytes of the current window, in order. */
  onChunk: (chunk: Uint8Array) => void;
  /** The current window's response ended without an error. */
  onResponseEnd?: () => void;
  /** The whole window streamed in and its range now counts as covered. */
  onWindowCommitted: (fresh: boolean) => void;
  /** A window failed or stalled; it is retried after a backoff. */
  onWindowFailed: () => void;
  logTag: string;
}

export interface SubtitleWindowFetcher {
  /** Fetches whatever the playhead needs next; see planSubtitleWindow. */
  maybeFetch: () => void;
  coverage: () => SubtitleWindowCoverage;
  /** Drops coverage before `start` once the caller has released it. */
  releaseBefore: (start: number) => void;
  dispose: () => void;
}

/**
 * Runs the sliding-window fetch loop for one subtitle track: one window on
 * the wire at a time, a stall timeout re-armed on every read, coverage
 * committed only once a window has streamed in whole, and a backoff after a
 * failure. The caller parses the bytes.
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
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let lastFetchFailureAt = 0;
  let retryDelay = 0;

  async function fetchWindow(seekStart: number, fresh: boolean) {
    // Only one window on the wire at a time: each fetch keeps an ffmpeg
    // process alive, so double-fetching wastes it.
    inflight?.abort();
    const controller = new AbortController();
    inflight = controller;
    inflightStart = seekStart;
    if (retryTimer !== null) clearTimeout(retryTimer);
    if (fresh) {
      coverageStart = seekStart;
      windowEnd = seekStart;
      atEOF = false;
    }
    options.onWindowStart(fresh);
    const requestedEnd = seekStart + SUBTITLE_WINDOW_SECONDS;

    // Abort the fetch if the response stops delivering chunks. Re-armed on
    // every read so a slow-but-progressing extraction is never cut off.
    let stallTimer: ReturnType<typeof setTimeout> | null = null;
    const armStallTimer = () => {
      if (stallTimer !== null) clearTimeout(stallTimer);
      stallTimer = setTimeout(() => controller.abort(), SUBTITLE_FETCH_STALL_TIMEOUT_MS);
    };

    const url = options.windowUrl(seekStart);
    let succeeded = false;
    try {
      armStallTimer();
      const response = await fetch(url, { signal: controller.signal });
      if (!response.ok || !response.body) {
        console.error(`[${options.logTag}] Failed to fetch ${url}: ${response.status}`);
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
      }
    } finally {
      if (stallTimer !== null) clearTimeout(stallTimer);
      const superseded = inflight !== controller;
      if (inflight === controller) inflight = null;
      if (succeeded && !disposed && !superseded) {
        hasFetched = true;
        retryDelay = 0;
        lastFetchFailureAt = 0;
        // Commit coverage only after the whole window streamed in. A failed
        // or stalled fetch must leave the range uncovered, or the gap would
        // read as fetched and never be retried.
        windowEnd = Math.max(windowEnd, requestedEnd);
        // End-of-input only when the window reaches the known media duration.
        // Where a window's cues stop says nothing: a window that ends inside a
        // dialogue gap looks the same as the end of the file.
        const duration = options.duration();
        if (duration > 0 && requestedEnd >= duration) atEOF = true;
        options.onWindowCommitted(fresh);
      } else if (!succeeded && !superseded && !disposed) {
        // A real failure (error, stall, or non-ok response) rather than a seek
        // superseding this fetch: back off before retrying.
        lastFetchFailureAt = Date.now();
        options.onWindowFailed();
        retryDelay = nextSubtitleRetryDelay(retryDelay);
        retryTimer = setTimeout(maybeFetch, retryDelay);
      }
    }
  }

  function maybeFetch() {
    if (disposed) return;
    const action = planSubtitleWindow(
      {
        hasFetched,
        coverageStart,
        windowEnd,
        atEOF,
        inflightStart: inflight ? inflightStart : null,
        failedAt: lastFetchFailureAt,
        retryDelayMs: retryDelay,
      },
      options.playhead(),
      Date.now(),
      options.seekBackoffSeconds,
    );
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
