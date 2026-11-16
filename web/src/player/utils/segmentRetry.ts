/**
 * Bounds the player's own recovery of a failed HLS load.
 *
 * hls.js retries a fragment a few times inside one load, then reports the
 * failure as fatal. The player answered that by starting the load again — and
 * on a stream that has already played, nothing counted those recoveries at
 * all. A corrupt source then asks the segment layer for the same fragment on
 * every recovery: one deployment fetched `seg_00000.ts` 147 times in a
 * two-minute window while the server logged a frame-RPS error for every
 * request. The stream could never succeed, so the retries only multiplied
 * server and relay load.
 *
 * The budget separates the two cases the recovery has to tell apart:
 *
 * - A transport hiccup. A failure on a segment the transport had not reached
 *   before is retried, and any load that completes clears the streak, so a
 *   long session can survive several unrelated blips.
 * - Repeated failure on the same segment. A fragment that keeps failing is not
 *   a hiccup, so its own count is capped well below the streak cap.
 *
 * Reaching either cap is a failed route: the caller surfaces the ordinary
 * failure UI (a replan) instead of requesting the segment again.
 */

/** Failures of one segment index before the load is treated as a failure. */
export const MAX_SAME_SEGMENT_FAILURES = 2;

/**
 * Failures without a completed load before the load is treated as a failure.
 * This catches a source where every segment fails at a different index, which
 * the per-segment cap alone would retry forever.
 */
export const MAX_STREAK_FAILURES = 3;

const BASE_RETRY_DELAY_MS = 1_000;
const MAX_RETRY_DELAY_MS = 8_000;

/**
 * How long to wait before the load's next attempt. The first retry is quick
 * enough to hide a blip; later ones back off so a failing source is not asked
 * for the same bytes at the rate hls.js would.
 */
export function segmentRetryDelayMs(attempt: number): number {
  const exponent = Number.isFinite(attempt) ? Math.max(0, Math.floor(attempt)) : 0;
  return Math.min(BASE_RETRY_DELAY_MS * 2 ** exponent, MAX_RETRY_DELAY_MS);
}

/**
 * Counts a transport's failed loads between completed ones.
 *
 * `key` identifies the fragment (its media sequence number), or is null for a
 * failure that belongs to the playlist or manifest rather than a segment.
 */
export class SegmentRetryBudget {
  private streak = 0;
  private readonly failures = new Map<string | null, number>();

  /**
   * Records a failed load of `key` and returns the retry attempt number to
   * back off with, or null when the transport has spent its budget and the
   * route has failed.
   */
  recordFailure(key: string | null): number | null {
    const sameSegment = (this.failures.get(key) ?? 0) + 1;
    this.failures.set(key, sameSegment);
    if (sameSegment >= MAX_SAME_SEGMENT_FAILURES) return null;
    if (this.streak >= MAX_STREAK_FAILURES) return null;
    return this.streak++;
  }

  /** A load that completed proves the transport works; the streak starts over. */
  reset(): void {
    this.streak = 0;
    this.failures.clear();
  }
}

/**
 * Failures allowed while a start has not drawn a single frame.
 *
 * A start that never renders is not replaying a blip: every failed segment is
 * another request against a plan that may never produce one. One deployment
 * fetched 1 061 segments in 85 s on such a plan before the startup deadline
 * ended it — server load with no possible success that also buried the real
 * failure. This budget holds the load ladder back inside the startup window so
 * the same plan asks for a handful of segments instead of all of them, and
 * ends the route once the budget is spent.
 */
export const MAX_STARTUP_SEGMENT_FAILURES = 10;

/**
 * Loads of the same fragment before it counts as an unappendable fragment.
 *
 * hls.js reloads a fragment when it cannot buffer it, and every one of those
 * loads can answer 200 with a perfectly valid segment: one deployment fetched
 * `seg_00000.ts` 199 times in a minute while the transport never produced a
 * frame. The load ladder only sees load *failures*, so those reloads were
 * invisible to it. Two loads of a fragment that buffered nothing in between
 * is not a prefetch; it is the transport discarding what it fetched.
 */
export const UNBUFFERED_FRAGMENT_LOADS = 2;

/**
 * Counts completed fragment loads that have not produced buffered media.
 *
 * A load only counts once the same fragment is fetched again with nothing
 * appended in between: the first load of a fragment is how every start fills
 * its buffer, and hls.js may legitimately ask for it once more while it waits
 * for an append. `recordBuffered` clears the fragment so a fragment that
 * eventually lands stops counting against the start.
 */
export class UnbufferedFragmentLoads {
  private readonly loads = new Map<string, number>();

  /** Returns how many times `key` has loaded since it last buffered. */
  recordLoad(key: string): number {
    const loads = (this.loads.get(key) ?? 0) + 1;
    this.loads.set(key, loads);
    return loads;
  }

  recordBuffered(key: string): void {
    this.loads.delete(key);
  }

  reset(): void {
    this.loads.clear();
  }
}

const STARTUP_RETRY_BASE_DELAY_MS = 1_000;
const STARTUP_RETRY_MAX_DELAY_MS = 8_000;

/**
 * What the caller should do with a failed HLS load while a start is unframed.
 *
 * - `ignore`: the transport has shown a frame, so this is not the startup
 *   window's problem; the caller keeps its ordinary retry cadence.
 * - `hold`: pause fetching for `delayMs` so the next attempt backs off.
 * - `stop`: the startup budget is spent; stop fetching and fail the route.
 */
export type StartupSegmentVerdict =
  | { kind: "ignore" }
  | { kind: "hold"; delayMs: number }
  | { kind: "stop" };

/**
 * Counts failed loads for one transport until it shows a frame.
 *
 * `recordFailure(framed)` takes the frame state from the caller because the
 * player's startup guard is the only thing that knows whether a frame reached
 * the screen. Once framed, every call reports `ignore`, so playback that
 * stalls after it started keeps the recovery it had.
 */
export class StartupSegmentBudget {
  private failures = 0;

  recordFailure(framed: boolean): StartupSegmentVerdict {
    if (framed) return { kind: "ignore" };

    this.failures += 1;
    if (this.failures > MAX_STARTUP_SEGMENT_FAILURES) return { kind: "stop" };

    // 1s, 2s, 4s, then the 8s cap: the startup window's load ladder, not
    // hls.js's per-fragment one.
    const delayMs = Math.min(
      STARTUP_RETRY_BASE_DELAY_MS * 2 ** (this.failures - 1),
      STARTUP_RETRY_MAX_DELAY_MS,
    );
    return { kind: "hold", delayMs };
  }
}
