import {
  BufferingQualityController,
  RENDERER_WAIT_BUFFER_MS,
  type BufferingAnswer,
  type BufferingEvent,
  type BufferingMode,
  type BufferingNotice,
  type BufferingPill,
  type BufferingPlan,
  type BufferingReplan,
  type BufferingSummary,
} from "../buffering-quality";
import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from "react";

/**
 * Feeds the buffering-quality controller from the video element and the wall
 * clock, and carries out what it decides: replans through the session, notices
 * and the prompt and undo pills. One controller lives for one title, so its
 * answers and its once-per-title notice survive replans and new sessions.
 */

/** How often the controller's timers are advanced. */
const TICK_MS = 100;
/**
 * hls.js steps over a small hole in the buffer by setting `currentTime` a
 * fraction of a second ahead while playback is starved. A seek that short and
 * forward during a stall is that, not the viewer's, and must not erase it.
 */
const GAP_SKIP_MAX_SECONDS = 1;

/** Stalls and pill timers use elapsed real time, not the system clock. */
const now = () => performance.now();

/**
 * The browser decoder holds little media past the playhead, so a wait while
 * it restarts its decoder is recognised from its own flag.
 */
function decoderRecovering(video: HTMLVideoElement): boolean {
  return (video as HTMLVideoElement & { decoderRecovering?: boolean }).decoderRecovering === true;
}

/** Media buffered past the playhead, in milliseconds; 0 when none is. */
export function bufferedAheadMs(video: HTMLVideoElement): number {
  const position = video.currentTime;
  const ranges = video.buffered;
  for (let i = 0; i < ranges.length; i++) {
    if (ranges.start(i) <= position && position < ranges.end(i))
      return (ranges.end(i) - position) * 1000;
  }
  return 0;
}

interface Options {
  videoRef: RefObject<HTMLVideoElement | null>;
  enabled: boolean;
  /**
   * Inside a Watch Party room, which has its own lower-quality offer: stalls
   * are still tracked and summarized, but the controller does not act.
   */
  suspended?: boolean;
  /** The title; a new one starts a new controller. */
  sessionId: string;
  /** The playback attempt; a new start within the title drops the kept estimate. */
  attemptId?: string | null;
  plan: BufferingPlan;
  /** Changes whenever a plan is adopted; an in-place update keeps it. */
  planRevision: number;
  mode: BufferingMode;
  /** A higher-priority pill (intro, segment skip, Next Up) is on screen. */
  obscured: boolean;
  /**
   * Whether the transport is failing rather than slow: requests erroring or
   * the browser offline. Buffering then is an outage, which the reconnect
   * path owns, so it never counts as a stall.
   */
  transportTrouble?: () => boolean;
  /**
   * Whether a newly adopted plan's transport will start playing on its own.
   * Its element starts paused and plays once enough data has arrived, so until
   * then the start is a wait the controller times, like a stall.
   */
  playIntent?: () => boolean;
  /**
   * Sends the replan; `onAdopt` names the revision it adopted, if any, and
   * `sent` is false when the session refused it before sending it.
   */
  onReplan: (
    request: BufferingReplan,
    onAdopt: (planId?: string, planRevision?: number, sent?: boolean) => void,
  ) => void;
  onNotice: (key: BufferingNotice) => void;
  /** `pageEnding` when the page is closing and the summary must go out at once. */
  onSummary?: (plan: BufferingPlan, summary: BufferingSummary, pageEnding: boolean) => void;
  /** The step-down estimate the client's own Auto replans repeat, or undefined. */
  onKeptEstimate?: (kbps: number | undefined) => void;
}

export interface BufferingPillState {
  planRevision: number;
  kind: BufferingPill;
  remainingMs: number;
  /** When the timer runs out, or null while it is frozen. */
  deadlineMs: number | null;
}

function samePill(a: BufferingPillState | null, b: BufferingPillState | null) {
  if (!a || !b) return a === b;
  return (
    a.planRevision === b.planRevision &&
    a.kind === b.kind &&
    a.deadlineMs === b.deadlineMs &&
    (a.deadlineMs !== null || a.remainingMs === b.remainingMs)
  );
}

/**
 * Dispatched on the element when hls.js finds the stream ended while the
 * element itself fires no `ended`; the wait that follows is the end of media.
 */
export const MEDIA_ENDED_EVENT = "silo:media-ended";

export function useAutoQualityDownshift(options: Options) {
  const latest = useRef(options);
  useEffect(() => {
    latest.current = options;
  });
  const controller = useRef<BufferingQualityController | null>(null);
  // The revision the controller's own replan adopted, so that adoption is not
  // mistaken for one the viewer or the server made.
  const requestedPlan = useRef<number | undefined>(undefined);
  const adoptedPlan = useRef<number | undefined>(undefined);
  const attempt = useRef<string | null | undefined>(undefined);
  // The adopted plan is starting with play intent and has not played yet.
  const starting = useRef(false);
  // The adopted plan has played. Until it has, a seek, the player's own
  // start-position seek included, does not end the start's wait.
  const planRan = useRef(false);
  // The page is closing, so the playing plan's summary is its last word.
  const pageEnding = useRef(false);
  const [pill, setPill] = useState<BufferingPillState | null>(null);
  // The pill state changes only when it shows, hides, freezes or resumes; the
  // pill counts down from its deadline itself.
  const refresh = useCallback(() => {
    const c = controller.current;
    const next = c?.pill
      ? {
          planRevision: latest.current.planRevision,
          kind: c.pill,
          remainingMs: c.remainingMs,
          deadlineMs: c.pillDeadline,
        }
      : null;
    setPill((current) => (samePill(current, next) ? current : next));
  }, []);
  useEffect(() => {
    if (!options.enabled) return;
    const c = new BufferingQualityController(
      latest.current.mode,
      (output) => {
        // Client replans are the session's own; only lowerings and Restore
        // are sent from here.
        if (output.type === "replan" && output.body.operation === "quality_change") {
          requestedPlan.current = undefined;
          latest.current.onReplan(output.body, (_id, revision, sent) => {
            if (controller.current !== c) return;
            requestedPlan.current = revision;
            if (sent === false) c.event({ type: "replan_failed", unsent: true }, now());
          });
        }
        if (output.type === "notice") latest.current.onNotice(output.key);
      },
      now(),
      (plan, summary) => latest.current.onSummary?.(plan, summary, pageEnding.current),
      (kbps) => latest.current.onKeptEstimate?.(kbps),
    );
    controller.current = c;
    // A closed tab, a reload or a navigation away ends the playing plan, and
    // nothing else reports it. A page restored from the back-forward cache
    // keeps playing unreported until its next adoption.
    const pagehide = () => {
      pageEnding.current = true;
      c.stop(now());
      pageEnding.current = false;
    };
    window.addEventListener("pagehide", pagehide);
    adoptedPlan.current = undefined;
    attempt.current = latest.current.attemptId;
    return () => {
      window.removeEventListener("pagehide", pagehide);
      c.stop(now());
      controller.current = null;
      // The next controller starts without a kept estimate and could never
      // discard this one, so the session stops repeating it.
      latest.current.onKeptEstimate?.(undefined);
    };
  }, [options.sessionId, options.enabled]);
  useEffect(() => {
    const c = controller.current;
    if (!c || options.attemptId === attempt.current) return;
    attempt.current = options.attemptId;
    c.event({ type: "new_attempt" }, now());
  }, [options.attemptId, options.enabled, options.sessionId]);
  useEffect(() => {
    const c = controller.current;
    if (!c) return;
    c.mode = options.mode;
    c.setSuspended(options.suspended ?? false, now());
    c.setObscured(options.obscured, now());
    const isNewPlan = adoptedPlan.current !== options.planRevision;
    adoptedPlan.current = options.planRevision;
    c.event(
      {
        type: "plan_adopted",
        plan: { ...options.plan, adoption_id: String(options.planRevision) },
        controllerInitiated: requestedPlan.current === options.planRevision,
      },
      now(),
    );
    if (isNewPlan) requestedPlan.current = undefined;
    const video = options.videoRef.current;
    if (isNewPlan) {
      starting.current = false;
      planRan.current = false;
    }
    if (isNewPlan && video) {
      // A plan adopted while the element is already playing fires no `playing`.
      if (!video.paused && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) {
        planRan.current = true;
        c.event({ type: "playing" }, now());
      } else if (
        latest.current.playIntent?.() &&
        video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA
      ) {
        // The element waits for data before it plays, and fires no `waiting`.
        starting.current = true;
        c.event({ type: "play" }, now());
        c.event({ type: "waiting" }, now());
      }
    }
  }, [
    options.plan,
    options.planRevision,
    options.mode,
    options.obscured,
    options.suspended,
    options.enabled,
    options.sessionId,
    options.videoRef,
  ]);
  useEffect(() => {
    const video = options.videoRef.current;
    const c = controller.current;
    if (!video || !c) return;
    let trouble = false;
    // Where playback stood when the current stall began. A stall can end
    // without a `playing` event (the player clears buffering on `timeupdate`),
    // so advancing time also ends it.
    let stalledAt: number | undefined;
    // A wait that began with media buffered, until playback resumes or the
    // buffer runs down and makes it a stall.
    let rendererWait = false;
    // Buffering after a seek or a source reload is theirs until playback runs
    // again, including a seek made while paused.
    let seekSincePlaying = false;
    let reloadSincePlaying = false;
    let lastPosition = video.currentTime;
    const syncTrouble = () => {
      const next = latest.current.transportTrouble?.() ?? false;
      if (next !== trouble)
        c.event({ type: next ? "transport_error_start" : "transport_error_end" }, now());
      trouble = next;
    };
    const send = (type: BufferingEvent["type"], cause?: BufferingEvent["cause"]) => {
      syncTrouble();
      c.event({ type, cause }, now());
      refresh();
    };
    const playing = () => {
      stalledAt = undefined;
      rendererWait = false;
      seekSincePlaying = false;
      reloadSincePlaying = false;
      starting.current = false;
      planRan.current = true;
      send("playing");
    };
    const rendererBusy = () =>
      decoderRecovering(video) || bufferedAheadMs(video) > RENDERER_WAIT_BUFFER_MS;
    const waitCause = (): BufferingEvent["cause"] => {
      // Until its first frame, a start the viewer means to play is the plan's
      // wait, though the transport emptied the element or a seek moved it on
      // the way and it has not begun to play yet; an explicit pause ends the
      // start. A renderer that is behind is still not the network.
      if (starting.current) return rendererBusy() ? "renderer" : undefined;
      if (planRan.current && (video.seeking || seekSincePlaying)) return "seek";
      if (reloadSincePlaying) return "reload";
      if (video.paused) return "pause";
      if (rendererBusy()) return "renderer";
      return undefined;
    };
    const waiting = () => {
      stalledAt = video.currentTime;
      const cause = waitCause();
      rendererWait = cause === "renderer";
      send("waiting", cause);
    };
    const pause = () => {
      rendererWait = false;
      starting.current = false;
      send("pause");
    };
    const play = () => send("play");
    const seek = () => {
      // Before the plan first runs, buffering after a seek, the player's
      // start-position seek included, still belongs to the start.
      if (!planRan.current) return;
      const forward = video.currentTime - (stalledAt ?? lastPosition);
      if (stalledAt !== undefined && forward >= 0 && forward <= GAP_SKIP_MAX_SECONDS) return;
      stalledAt = undefined;
      rendererWait = false;
      seekSincePlaying = true;
      send("seek");
    };
    const reload = () => {
      stalledAt = undefined;
      rendererWait = false;
      reloadSincePlaying = true;
      send("reload");
      // The new transport empties the element as it attaches; its start is
      // still the plan's wait.
      if (starting.current) send("waiting");
    };
    const timeupdate = () => {
      lastPosition = video.currentTime;
      if (stalledAt !== undefined && video.currentTime > stalledAt) playing();
    };
    const events = {
      playing,
      play,
      waiting,
      pause,
      seeking: seek,
      emptied: reload,
      ended: pause,
      // hls.js stalled at the end of the stream without the element ending.
      [MEDIA_ENDED_EVENT]: pause,
      timeupdate,
    };
    for (const [name, handler] of Object.entries(events)) video.addEventListener(name, handler);
    if (!video.paused && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) playing();
    const timer = setInterval(() => {
      syncTrouble();
      if (rendererWait && !rendererBusy()) {
        rendererWait = false;
        c.event({ type: "waiting" }, now());
      }
      c.tick(now());
      refresh();
    }, TICK_MS);
    return () => {
      clearInterval(timer);
      for (const [name, handler] of Object.entries(events))
        video.removeEventListener(name, handler);
    };
  }, [options.videoRef, options.enabled, options.sessionId, refresh]);
  const answer = useCallback(
    (type: BufferingAnswer | "quality_changed") => {
      controller.current?.event({ type }, now());
      refresh();
    },
    [refresh],
  );
  const visiblePill =
    options.enabled && !options.obscured && pill?.planRevision === options.planRevision
      ? pill
      : null;
  return useMemo(() => ({ pill: visiblePill, answer }), [visiblePill, answer]);
}
