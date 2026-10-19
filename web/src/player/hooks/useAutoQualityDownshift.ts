import { useEffect, useRef, type RefObject } from "react";

/**
 * Auto quality plays the source unless a bandwidth estimate taken before
 * playback says the link cannot carry it, and a transcode is one HLS
 * rendition, so nothing below the server lowers quality when the link turns
 * out slower. This watches for playback that keeps rebuffering and asks for a
 * plan sized below what it could not sustain.
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
 * A downshift whose replan was not adopted leaves the same plan playing. After
 * this long the hook watches it again, so a transient replan failure does not
 * end automatic downshifting for the rest of the plan.
 */
const REARM_AFTER_UNADOPTED_MS = 60_000;
/**
 * How long a plan that came back no lower after a downshift is left alone.
 * The plan may be the bottom of the ladder, or another replan's result that
 * arrived first; after this it is watched again either way.
 */
const NO_LOWER_COOLDOWN_MS = 5 * 60_000;
/**
 * The new estimate as a share of the bitrate that stalled. Auto plans for 80%
 * of an estimate, so the next stream targets about 56% of the one that failed:
 * a 4K remux steps to a 4K encode, a 1080p encode to 720p.
 */
export const DOWNSHIFT_ESTIMATE_RATIO = 0.7;

/** How often a stall in progress is re-checked once the long-stall timer fires. */
const STALL_RECHECK_MS = 1_000;

export interface StallTracker {
  /** The plan began playing. */
  playing(now: number): void;
  /** The element ran out of data. Seeks are excluded by the caller. */
  waiting(now: number): void;
  /**
   * A seek, pause or reload began; a stall in progress was caused by that,
   * not by the network.
   */
  seeking(): void;
  /** Whether stall history so far says the link cannot keep up. */
  shouldDownshift(now: number): boolean;
}

export function createStallTracker(): StallTracker {
  let firstPlayingAt: number | undefined;
  // When the plan first ran out of data, if that happened before its first
  // frame. A start that never gets going is timed from here instead.
  let firstWaitingAt: number | undefined;
  let stallStartedAt: number | undefined;
  let stalls: number[] = [];
  // When a stall that reached LONG_STALL_MS ended. It still counts as long,
  // since its timer can run late after playback recovered, but only within
  // the stall window like any other stall.
  let longStallEndedAt: number | undefined;
  return {
    playing(now) {
      firstPlayingAt ??= now;
      if (stallStartedAt !== undefined) {
        const duration = now - stallStartedAt;
        if (duration >= LONG_STALL_MS) longStallEndedAt = now;
        else if (duration >= STALL_MIN_MS) stalls.push(now);
      }
      stallStartedAt = undefined;
    },
    waiting(now) {
      // The grace period runs from the first frame, or from the first wait
      // when the plan has not produced one. A stall that begins during it
      // counts only from its end, so one that outlasts it is still seen.
      const graceFrom = firstPlayingAt ?? (firstWaitingAt ??= now);
      stallStartedAt ??= Math.max(now, graceFrom + STARTUP_GRACE_MS);
    },
    seeking() {
      stallStartedAt = undefined;
    },
    shouldDownshift(now) {
      if (longStallEndedAt !== undefined && now - longStallEndedAt < STALL_WINDOW_MS) return true;
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
  /**
   * Whether the transport is failing rather than slow: requests erroring or
   * the browser offline. Buffering then is an outage, which the reconnect
   * path owns, so it never counts toward a downshift.
   */
  transportTrouble?: () => boolean;
  onDownshift: (estimateKbps: number) => void;
}

export function useAutoQualityDownshift({
  videoRef,
  enabled,
  sessionId,
  planIdentity,
  deliveredKbps,
  onDownshift,
  transportTrouble,
}: Options): void {
  const onDownshiftRef = useRef(onDownshift);
  const transportTroubleRef = useRef(transportTrouble);
  useEffect(() => {
    onDownshiftRef.current = onDownshift;
    transportTroubleRef.current = transportTrouble;
  }, [onDownshift, transportTrouble]);
  // The plan the last downshift left and its bitrate, until the next plan is
  // adopted. The same plan still playing means the replan was not adopted,
  // and it stays eligible.
  const lastDownshiftFromRef = useRef<
    { sessionId: string; planIdentity: unknown; kbps: number } | undefined
  >(undefined);
  // A plan adopted after a downshift that turned out no lower.
  const exhaustedPlanRef = useRef<
    { sessionId: string; planIdentity: unknown; until: number } | undefined
  >(undefined);

  useEffect(() => {
    const video = videoRef.current;
    if (!enabled || !video || deliveredKbps === undefined) return;
    const estimate = downshiftEstimateKbps(deliveredKbps);
    if (estimate === undefined) return;
    // The first plan adopted after a downshift settles it. No lower than the
    // plan that stalled means the ladder may have nothing further down, or
    // another replan's plan arrived first: leave it alone for a while rather
    // than replan in a loop. Lower means the step worked, and any later plan,
    // even one an unrelated replan raised again, is watched as usual.
    const previous = lastDownshiftFromRef.current;
    if (previous?.sessionId === sessionId && previous.planIdentity !== planIdentity) {
      lastDownshiftFromRef.current = undefined;
      if (deliveredKbps >= previous.kbps) {
        exhaustedPlanRef.current = {
          sessionId,
          planIdentity,
          until: Date.now() + NO_LOWER_COOLDOWN_MS,
        };
      }
    }
    const exhausted = exhaustedPlanRef.current;
    const blockedUntil =
      exhausted?.sessionId === sessionId && exhausted.planIdentity === planIdentity
        ? exhausted.until
        : 0;

    // Where playback stood when the current stall began; undefined when not
    // stalled. A stall can end without a `playing` event (the player itself
    // clears buffering on `timeupdate`), so advancing time also ends it.
    let stalledAt: number | undefined;
    let tracker = createStallTracker();
    let done = false;
    let longStallTimer: ReturnType<typeof setTimeout> | undefined;
    let rearmTimer: ReturnType<typeof setTimeout> | undefined;
    const clearLongStallTimer = () => {
      if (longStallTimer !== undefined) clearTimeout(longStallTimer);
      longStallTimer = undefined;
    };
    // A seek, pause, end or source reload ends any stall in progress: the
    // buffering that follows is not the link failing to keep up.
    const onInterrupted = () => {
      stalledAt = undefined;
      clearLongStallTimer();
      tracker.seeking();
    };
    const check = () => {
      if (done || transportTroubleRef.current?.() || Date.now() < blockedUntil) return;
      // A paused element is not starved, whatever happened before the pause.
      if (video.paused || !tracker.shouldDownshift(Date.now())) return;
      done = true;
      clearLongStallTimer();
      lastDownshiftFromRef.current = { sessionId, planIdentity, kbps: deliveredKbps };
      // An adopted replan changes planIdentity and tears this effect down,
      // cancelling the re-arm. Still being here means it was not adopted.
      rearmTimer = setTimeout(() => {
        rearmTimer = undefined;
        done = false;
        tracker = createStallTracker();
        tracker.playing(Date.now() - STARTUP_GRACE_MS);
        lastDownshiftFromRef.current = undefined;
        // A stall still in progress fires no further `waiting`; time it
        // again from here so it can downshift once more.
        if (stalledAt !== undefined) {
          tracker.waiting(Date.now());
          clearLongStallTimer();
          longStallTimer = setTimeout(checkStall, LONG_STALL_MS);
        }
      }, REARM_AFTER_UNADOPTED_MS);
      onDownshiftRef.current(estimate);
    };
    const onPlaying = () => {
      // A stall that overlapped an outage was the outage; do not count it.
      if (transportTroubleRef.current?.()) tracker.seeking();
      stalledAt = undefined;
      clearLongStallTimer();
      tracker.playing(Date.now());
      check();
    };
    // Keeps looking while the stall lasts: one that began in the startup grace
    // period only becomes long enough some time after the first check.
    const checkStall = () => {
      longStallTimer = undefined;
      // During an outage, time the stall again from when it clears, so a
      // stall that outlasts the outage still counts.
      if (transportTroubleRef.current?.() && stalledAt !== undefined) {
        tracker.seeking();
        tracker.waiting(Date.now());
      }
      check();
      if (!done && stalledAt !== undefined)
        longStallTimer = setTimeout(checkStall, STALL_RECHECK_MS);
    };
    const onWaiting = () => {
      if (video.seeking || video.paused) return;
      stalledAt = video.currentTime;
      tracker.waiting(Date.now());
      clearLongStallTimer();
      longStallTimer = setTimeout(checkStall, LONG_STALL_MS);
    };
    const onTimeUpdate = () => {
      if (stalledAt !== undefined && video.currentTime > stalledAt) onPlaying();
    };
    // A plan adopted while the element is already playing fires no `playing`.
    if (!video.paused && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) {
      tracker.playing(Date.now());
    }
    video.addEventListener("playing", onPlaying);
    video.addEventListener("waiting", onWaiting);
    const interruptions = ["seeking", "pause", "ended", "emptied"] as const;
    for (const type of interruptions) video.addEventListener(type, onInterrupted);
    video.addEventListener("timeupdate", onTimeUpdate);
    return () => {
      clearLongStallTimer();
      if (rearmTimer !== undefined) clearTimeout(rearmTimer);
      video.removeEventListener("playing", onPlaying);
      video.removeEventListener("waiting", onWaiting);
      for (const type of interruptions) video.removeEventListener(type, onInterrupted);
      video.removeEventListener("timeupdate", onTimeUpdate);
    };
  }, [videoRef, enabled, sessionId, planIdentity, deliveredKbps]);
}
