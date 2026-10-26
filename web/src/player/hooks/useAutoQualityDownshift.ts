import {
  BufferingQualityController,
  OUTAGE_NO_DATA_MS,
  OUTAGE_NO_DATA_SEGMENTED_MS,
  RENDERER_WAIT_AMPLE_MS,
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
   * When the plan's media requests last received data, on the
   * `performance.now()` clock, and whether they are segments, or undefined
   * where the player cannot see their bytes (the media element's own loads).
   * Read on every tick, so a transport that is polled can time the data it
   * finds.
   */
  mediaData?: () => { at: number; segmented: boolean } | undefined;
  /**
   * The current plan's measured throughput, or undefined where the player
   * cannot measure it. A start deadline, and any stall before the attempt
   * first plays, lowers only when it is below the plan's bitrate.
   */
  throughputKbps?: () => number | undefined;
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
    onAdopt: (planRevision?: number, sent?: boolean) => void,
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
  // How far the current attempt has got. A plan adopted before any of its
  // plans played (a failure recovery, or a lowering during the start wait)
  // continues the attempt's start; one adopted after replaces a source that
  // played, and its load is a source reload.
  const attemptStage = useRef<"new" | "adopted" | "played">("new");
  // The attempt is starting with play intent and has not played yet.
  const starting = useRef(false);
  // A source reload: the transport was emptied or replaced, and playback has
  // not run since. Buffering meanwhile is the reload's, never a stall.
  const reloading = useRef(false);
  // The adopted plan has played. Until it has, a seek, the player's own
  // start-position seek included, does not end the start's wait.
  const planRan = useRef(false);
  // The wait is the renderer's, not the network's. One that began with media
  // buffered becomes a stall if the buffer runs down; one that came to hold
  // plenty stays the renderer's until playback runs.
  const rendererWait = useRef<"buffered" | "ample" | undefined>(undefined);
  // The page is closing, so the playing plan's summary is its last word.
  const pageEnding = useRef(false);
  // The failure a start deadline's lowering stands in for, while it is sent.
  const deadlineFailure = useRef<(() => void) | undefined>(undefined);
  const [pill, setPill] = useState<BufferingPillState | null>(null);
  // Playback waits through an outage: the picture is frozen until it runs.
  const [outage, setOutage] = useState(false);
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
    const c = new BufferingQualityController(
      latest.current.mode,
      (output) => {
        // Client replans are the session's own; only lowerings and Restore
        // are sent from here.
        if (output.type === "replan" && output.body.operation === "quality_change") {
          requestedPlan.current = undefined;
          const fail = deadlineFailure.current;
          const failedPlan = latest.current.planRevision;
          latest.current.onReplan(output.body, (revision, sent) => {
            if (controller.current !== c) return;
            requestedPlan.current = revision;
            if (sent === false) c.event({ type: "replan_failed", unsent: true }, now());
            // The plan that missed its deadline is dead; without a lower
            // plan to replace it, it fails as it would have, unless another
            // plan has replaced it meanwhile.
            if (revision === undefined && latest.current.planRevision === failedPlan) fail?.();
          });
        }
        if (output.type === "notice") latest.current.onNotice(output.key);
      },
      now(),
      (plan, summary) => latest.current.onSummary?.(plan, summary, pageEnding.current),
      (kbps) => latest.current.onKeptEstimate?.(kbps),
      () => latest.current.throughputKbps?.(),
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
    // A page back from the back-forward cache plays on: its plan is adopted
    // again as a new adoption of the same attempt, with its own grace and
    // summary, and the title's answer and kept estimate carry over.
    let restores = 0;
    const pageshow = (event: PageTransitionEvent) => {
      if (!event.persisted) return;
      const { plan, planRevision, videoRef } = latest.current;
      restores += 1;
      c.event(
        {
          type: "plan_adopted",
          plan: { ...plan, adoption_id: `${planRevision}.${restores}` },
          controllerInitiated: false,
        },
        now(),
      );
      const video = videoRef.current;
      if (video && !video.paused && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA)
        c.event({ type: "playing" }, now());
      refresh();
    };
    window.addEventListener("pagehide", pagehide);
    window.addEventListener("pageshow", pageshow);
    adoptedPlan.current = undefined;
    attempt.current = latest.current.attemptId;
    attemptStage.current = "new";
    return () => {
      window.removeEventListener("pagehide", pagehide);
      window.removeEventListener("pageshow", pageshow);
      c.stop(now());
      controller.current = null;
      // The next controller starts without a kept estimate and could never
      // discard this one, so the session stops repeating it.
      latest.current.onKeptEstimate?.(undefined);
    };
  }, [options.sessionId, refresh]);
  useEffect(() => {
    const c = controller.current;
    if (!c || options.attemptId === attempt.current) return;
    attempt.current = options.attemptId;
    attemptStage.current = "new";
    c.event({ type: "new_attempt" }, now());
  }, [options.attemptId, options.sessionId]);
  useEffect(() => {
    const c = controller.current;
    if (!c) return;
    c.mode = options.mode;
    c.setSuspended(options.suspended ?? false, now());
    c.setObscured(options.obscured, now());
    const isNewPlan = adoptedPlan.current !== options.planRevision;
    adoptedPlan.current = options.planRevision;
    const stage = attemptStage.current;
    c.event(
      {
        type: "plan_adopted",
        plan: { ...options.plan, adoption_id: String(options.planRevision) },
        controllerInitiated: requestedPlan.current === options.planRevision,
        continuesStart: isNewPlan && stage === "adopted",
      },
      now(),
    );
    if (isNewPlan) requestedPlan.current = undefined;
    const video = options.videoRef.current;
    if (isNewPlan) {
      starting.current = false;
      planRan.current = false;
      rendererWait.current = undefined;
      if (stage === "new") attemptStage.current = "adopted";
    }
    if (isNewPlan && video) {
      // A plan adopted while the element is already playing fires no `playing`.
      if (!video.paused && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) {
        planRan.current = true;
        attemptStage.current = "played";
        c.event({ type: "playing" }, now());
      } else if (
        latest.current.playIntent?.() &&
        video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA
      ) {
        if (stage !== "played") {
          // The element waits for data before it plays, and fires no `waiting`.
          starting.current = true;
          c.event({ type: "play" }, now());
          c.event({ type: "waiting" }, now());
        } else {
          // A replacement (a track or quality change, a lowering) replaces
          // the source that played: its load is a source reload until it
          // plays. One that never starts is the start deadline's.
          reloading.current = true;
        }
      }
    }
  }, [
    options.plan,
    options.planRevision,
    options.mode,
    options.obscured,
    options.suspended,
    options.sessionId,
    options.videoRef,
  ]);
  useEffect(() => {
    const video = options.videoRef.current;
    const c = controller.current;
    if (!video || !c) return;
    let trouble = false;
    // A wait for data that an outage held. It lasts until the wait ends,
    // though the outage may end first: data is back, playback is not yet.
    let outageWait = false;
    // Where playback stood when the current stall began. A stall can end
    // without a `playing` event (the player clears buffering on `timeupdate`),
    // so advancing time also ends it.
    let stalledAt: number | undefined;
    // Buffering after a seek or a source reload is theirs until playback runs
    // again, including a seek made while paused.
    let seekSincePlaying = false;
    reloading.current = false;
    let lastPosition = video.currentTime;
    const syncTrouble = () => {
      // A wait on a link that has gone silent is an outage too; one whose
      // data keeps arriving, however slowly, is a stall. The server holds a
      // segment silent while it produces it, so segments get longer. A source
      // reload's wait is not timed, but a silent one is an outage all the
      // same, which the start deadline needs to know.
      const data = latest.current.mediaData?.();
      const silent =
        (c.waitingForData || reloading.current) &&
        data !== undefined &&
        now() - data.at >= (data.segmented ? OUTAGE_NO_DATA_SEGMENTED_MS : OUTAGE_NO_DATA_MS);
      const next = (latest.current.transportTrouble?.() ?? false) || silent;
      if (next !== trouble)
        c.event({ type: next ? "transport_error_start" : "transport_error_end" }, now());
      trouble = next;
    };
    const syncOutageWait = () => {
      const next = c.waitingForData && (outageWait || trouble);
      if (next !== outageWait) setOutage(next);
      outageWait = next;
    };
    const send = (type: BufferingEvent["type"], cause?: BufferingEvent["cause"]) => {
      syncTrouble();
      c.event({ type, cause }, now());
      // A wait that begins on a link already silent is an outage at once.
      if (type === "waiting") syncTrouble();
      syncOutageWait();
      refresh();
    };
    const playing = () => {
      stalledAt = undefined;
      rendererWait.current = undefined;
      seekSincePlaying = false;
      reloading.current = false;
      starting.current = false;
      planRan.current = true;
      attemptStage.current = "played";
      send("playing");
    };
    const rendererBusy = () =>
      rendererWait.current === "ample" ||
      decoderRecovering(video) ||
      bufferedAheadMs(video) > RENDERER_WAIT_BUFFER_MS;
    const waitCause = (): BufferingEvent["cause"] => {
      // Until its first frame, a start the viewer means to play is the plan's
      // wait, though the transport emptied the element or a seek moved it on
      // the way and it has not begun to play yet; an explicit pause ends the
      // start. A renderer that is behind is still not the network.
      if (starting.current) return rendererBusy() ? "renderer" : undefined;
      if (planRan.current && (video.seeking || seekSincePlaying)) return "seek";
      if (reloading.current) return "reload";
      if (video.paused) return "pause";
      if (rendererBusy()) return "renderer";
      return undefined;
    };
    const waiting = () => {
      stalledAt = video.currentTime;
      const cause = waitCause();
      if (rendererWait.current !== "ample")
        rendererWait.current = cause === "renderer" ? "buffered" : undefined;
      send("waiting", cause);
    };
    const pause = () => {
      rendererWait.current = undefined;
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
      rendererWait.current = undefined;
      seekSincePlaying = true;
      send("seek");
    };
    const reload = () => {
      stalledAt = undefined;
      rendererWait.current = undefined;
      reloading.current = true;
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
      const wait = rendererWait.current;
      if (
        wait !== "ample" &&
        (wait || c.waitingForData || (reloading.current && latest.current.playIntent?.())) &&
        bufferedAheadMs(video) > RENDERER_WAIT_AMPLE_MS
      ) {
        // Plenty buffered makes any wait the renderer's until playback runs;
        // a stall so far ends uncounted. A reload's wait is never counted,
        // but it is latched here, before a start deadline empties the element.
        rendererWait.current = "ample";
        if (c.waitingForData) send("waiting", "renderer");
      } else if (wait === "buffered" && !rendererBusy()) {
        rendererWait.current = undefined;
        c.event({ type: "waiting" }, now());
      }
      c.tick(now());
      syncOutageWait();
      refresh();
    }, TICK_MS);
    return () => {
      clearInterval(timer);
      for (const [name, handler] of Object.entries(events))
        video.removeEventListener(name, handler);
      setOutage(false);
    };
  }, [options.videoRef, options.sessionId, refresh]);
  const answer = useCallback(
    (type: BufferingAnswer | "quality_changed" | "pick_sent" | "pick_refused") => {
      controller.current?.event({ type }, now());
      refresh();
    },
    [refresh],
  );
  /**
   * The player's start deadline passed before the plan's first frame. When
   * Auto lowers on its own and the plan's measured throughput is below its
   * bitrate, the controller lowers instead (see its startDeadlinePassed);
   * `fail` reports the plan's failure when it does not, or when its lowering
   * is not adopted. A start held up by the renderer, not the network, fails
   * without lowering.
   */
  const startDeadlinePassed = useCallback(
    (fail: () => void) => {
      if (rendererWait.current) {
        fail();
        return;
      }
      deadlineFailure.current = fail;
      const lowered = controller.current?.startDeadlinePassed(now()) ?? false;
      deadlineFailure.current = undefined;
      refresh();
      if (!lowered) fail();
    },
    [refresh],
  );
  const visiblePill =
    !options.obscured && pill?.planRevision === options.planRevision ? pill : null;
  return useMemo(
    () => ({ pill: visiblePill, outage, answer, startDeadlinePassed }),
    [visiblePill, outage, answer, startDeadlinePassed],
  );
}
