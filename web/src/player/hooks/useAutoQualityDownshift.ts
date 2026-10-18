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

/** Media buffered past the playhead, in milliseconds; 0 when none is. */
function bufferedAheadMs(video: HTMLVideoElement): number {
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
  /** Off inside a Watch Party room, which has its own lower-quality offer. */
  enabled: boolean;
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
  /** Sends the replan; `onAdopt` names the revision it adopted, if any. */
  onReplan: (
    request: BufferingReplan,
    onAdopt: (planId?: string, planRevision?: number) => void,
  ) => void;
  onNotice: (key: BufferingNotice) => void;
  onSummary?: (plan: BufferingPlan, summary: BufferingSummary) => void;
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
          latest.current.onReplan(output.body, (_id, revision) => {
            if (controller.current === c) requestedPlan.current = revision;
          });
        }
        if (output.type === "notice") latest.current.onNotice(output.key);
      },
      Date.now(),
      (plan, summary) => latest.current.onSummary?.(plan, summary),
      (kbps) => latest.current.onKeptEstimate?.(kbps),
    );
    controller.current = c;
    adoptedPlan.current = undefined;
    attempt.current = latest.current.attemptId;
    return () => {
      c.stop(Date.now());
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
    c.event({ type: "new_attempt" }, Date.now());
  }, [options.attemptId, options.enabled, options.sessionId]);
  useEffect(() => {
    const c = controller.current;
    if (!c) return;
    c.mode = options.mode;
    c.setObscured(options.obscured, Date.now());
    const isNewPlan = adoptedPlan.current !== options.planRevision;
    adoptedPlan.current = options.planRevision;
    c.event(
      {
        type: "plan_adopted",
        plan: { ...options.plan, adoption_id: String(options.planRevision) },
        controllerInitiated: requestedPlan.current === options.planRevision,
      },
      Date.now(),
    );
    if (isNewPlan) requestedPlan.current = undefined;
    // A plan adopted while the element is already playing fires no `playing`.
    const video = options.videoRef.current;
    if (
      isNewPlan &&
      video &&
      !video.paused &&
      video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA
    )
      c.event({ type: "playing" }, Date.now());
  }, [
    options.plan,
    options.planRevision,
    options.mode,
    options.obscured,
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
    const syncTrouble = () => {
      const next = latest.current.transportTrouble?.() ?? false;
      if (next !== trouble)
        c.event({ type: next ? "transport_error_start" : "transport_error_end" }, Date.now());
      trouble = next;
    };
    const send = (type: BufferingEvent["type"], cause?: BufferingEvent["cause"]) => {
      syncTrouble();
      c.event({ type, cause }, Date.now());
      refresh();
    };
    const playing = () => {
      stalledAt = undefined;
      rendererWait = false;
      send("playing");
    };
    const waiting = () => {
      stalledAt = video.currentTime;
      const cause = video.seeking
        ? "seek"
        : video.paused
          ? "pause"
          : bufferedAheadMs(video) > RENDERER_WAIT_BUFFER_MS
            ? "renderer"
            : undefined;
      rendererWait = cause === "renderer";
      send("waiting", cause);
    };
    const pause = () => {
      rendererWait = false;
      send("pause");
    };
    const play = () => send("play");
    const seek = () => {
      stalledAt = undefined;
      rendererWait = false;
      send("seek");
    };
    const reload = () => {
      stalledAt = undefined;
      rendererWait = false;
      send("reload");
    };
    const timeupdate = () => {
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
      timeupdate,
    };
    for (const [name, handler] of Object.entries(events)) video.addEventListener(name, handler);
    if (!video.paused && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) playing();
    const timer = setInterval(() => {
      syncTrouble();
      if (rendererWait && bufferedAheadMs(video) <= RENDERER_WAIT_BUFFER_MS) {
        rendererWait = false;
        c.event({ type: "waiting" }, Date.now());
      }
      c.tick(Date.now());
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
      controller.current?.event({ type }, Date.now());
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
