/**
 * The sliding-window schedule shared by the subtitle fetchers
 * (useSubtitleTracks for text, usePGSSubtitles for bitmaps). An embedded track
 * can take minutes to extract whole from a large remux, while a positioned
 * window comes back in a second or two, so each fetcher asks the server for
 * bounded source-time windows around the playhead.
 */

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
 *     overlapping the previous one slightly;
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
    return {
      kind: "extend",
      start: Math.max(state.windowEnd - SUBTITLE_WINDOW_OVERLAP_SECONDS, time),
    };
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
