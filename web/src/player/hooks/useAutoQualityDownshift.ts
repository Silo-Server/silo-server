import { useEffect, useRef, type RefObject } from "react";

/**
 * Auto quality picks a stream from a bandwidth estimate taken before playback,
 * and a transcode is one HLS rendition, so nothing below the server lowers
 * quality when the link turns out slower. This watches for playback that keeps
 * rebuffering and asks for a plan sized below what it could not sustain.
 */

/** Buffering right after a plan starts is startup, not a slow link. */
const STARTUP_GRACE_MS = 10_000;
/** A stall shorter than this is ordinary segment-boundary buffering. */
const STALL_MIN_MS = 1_000;
/** This many counted stalls within the window means the link cannot keep up. */
const STALLS_BEFORE_DOWNSHIFT = 2;
const STALL_WINDOW_MS = 2 * 60_000;
/** One stall this long is enough on its own. */
const LONG_STALL_MS = 6_000;
/**
 * The new estimate as a share of the bitrate that stalled. Auto plans for 80%
 * of an estimate, so the next stream targets about 56% of the one that failed:
 * a 4K remux steps to a 4K encode, a 1080p encode to 720p.
 */
export const DOWNSHIFT_ESTIMATE_RATIO = 0.7;

export interface StallTracker {
  /** The plan began playing. */
  playing(now: number): void;
  /** The element ran out of data. Seeks are excluded by the caller. */
  waiting(now: number): void;
  /** A seek began; a stall in progress was the seek's, not the network's. */
  seeking(): void;
  /** Whether stall history so far says the link cannot keep up. */
  shouldDownshift(now: number): boolean;
}

export function createStallTracker(): StallTracker {
  let firstPlayingAt: number | undefined;
  let stallStartedAt: number | undefined;
  let stalls: number[] = [];
  return {
    playing(now) {
      firstPlayingAt ??= now;
      if (stallStartedAt !== undefined && now - stallStartedAt >= STALL_MIN_MS) stalls.push(now);
      stallStartedAt = undefined;
    },
    waiting(now) {
      if (firstPlayingAt === undefined || now - firstPlayingAt < STARTUP_GRACE_MS) return;
      stallStartedAt ??= now;
    },
    seeking() {
      stallStartedAt = undefined;
    },
    shouldDownshift(now) {
      if (stallStartedAt !== undefined && now - stallStartedAt >= LONG_STALL_MS) return true;
      stalls = stalls.filter((at) => now - at < STALL_WINDOW_MS);
      return stalls.length >= STALLS_BEFORE_DOWNSHIFT;
    },
  };
}

/** The estimate to plan with after `deliveredKbps` stalled, or undefined. */
export function downshiftEstimateKbps(deliveredKbps: number | undefined): number | undefined {
  if (deliveredKbps === undefined || !Number.isFinite(deliveredKbps) || deliveredKbps <= 0) {
    return undefined;
  }
  const estimate = Math.floor(deliveredKbps * DOWNSHIFT_ESTIMATE_RATIO);
  return estimate >= 100 ? estimate : undefined;
}

interface Options {
  videoRef: RefObject<HTMLVideoElement | null>;
  /** Only Auto quality is ours to change; an explicit choice is the viewer's. */
  enabled: boolean;
  /** The playback session; a new one starts with no downshift history. */
  sessionId: string;
  /** Changes whenever a new plan is adopted; stall history is per plan. */
  planIdentity: unknown;
  /** Bitrate the current plan delivers, when the server reported it. */
  deliveredKbps: number | undefined;
  onDownshift: (estimateKbps: number) => void;
}

export function useAutoQualityDownshift({
  videoRef,
  enabled,
  sessionId,
  planIdentity,
  deliveredKbps,
  onDownshift,
}: Options): void {
  const onDownshiftRef = useRef(onDownshift);
  useEffect(() => {
    onDownshiftRef.current = onDownshift;
  }, [onDownshift]);
  // The bitrate the last downshift left. A plan that is no lower means the
  // ladder has nothing further down, so asking again would only loop.
  const lastDownshiftFromRef = useRef<{ sessionId: string; kbps: number } | undefined>(undefined);

  useEffect(() => {
    const video = videoRef.current;
    if (!enabled || !video || deliveredKbps === undefined) return;
    const estimate = downshiftEstimateKbps(deliveredKbps);
    if (estimate === undefined) return;
    const previous = lastDownshiftFromRef.current;
    if (previous?.sessionId === sessionId && deliveredKbps >= previous.kbps) return;

    const tracker = createStallTracker();
    let done = false;
    let longStallTimer: ReturnType<typeof setTimeout> | undefined;
    const clearLongStallTimer = () => {
      if (longStallTimer !== undefined) clearTimeout(longStallTimer);
      longStallTimer = undefined;
    };
    const check = () => {
      if (done || !tracker.shouldDownshift(Date.now())) return;
      done = true;
      clearLongStallTimer();
      lastDownshiftFromRef.current = { sessionId, kbps: deliveredKbps };
      onDownshiftRef.current(estimate);
    };
    const onPlaying = () => {
      clearLongStallTimer();
      tracker.playing(Date.now());
      check();
    };
    const onWaiting = () => {
      if (video.seeking || video.paused) return;
      tracker.waiting(Date.now());
      clearLongStallTimer();
      longStallTimer = setTimeout(check, LONG_STALL_MS);
    };
    const onSeeking = () => {
      clearLongStallTimer();
      tracker.seeking();
    };
    // A plan adopted while the element is already playing fires no `playing`.
    if (!video.paused && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) {
      tracker.playing(Date.now());
    }
    video.addEventListener("playing", onPlaying);
    video.addEventListener("waiting", onWaiting);
    video.addEventListener("seeking", onSeeking);
    return () => {
      clearLongStallTimer();
      video.removeEventListener("playing", onPlaying);
      video.removeEventListener("waiting", onWaiting);
      video.removeEventListener("seeking", onSeeking);
    };
  }, [videoRef, enabled, sessionId, planIdentity, deliveredKbps]);
}
