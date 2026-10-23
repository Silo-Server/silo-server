/**
 * The buffering-quality controller (docs/design/2026-10-17-buffering-quality.md).
 *
 * It watches the plans of one title for repeated rebuffering and decides what
 * happens next: lower Auto at once and offer an undo, ask first, or only
 * report. It owns no timers and no media element; the adapter
 * (useAutoQualityDownshift) feeds it media events and the wall clock and
 * carries out the replans and pills it emits. That keeps it replayable against
 * the shared conformance vectors, which every client runs unchanged.
 *
 * It also keeps the step-down estimate of the latest Auto lowering, which the
 * client's own later Auto replans repeat so the plan stays lowered. The
 * session sends those replans; the controller reports the estimate through
 * `onKeptEstimate` and answers `client_replan` for the vectors.
 */
import type {
  BufferingMode,
  BufferingPlan,
  BufferingReplan,
  ClientReplan,
  BufferingPill,
  BufferingOutput,
  BufferingEvent,
  BufferingAction,
  BufferingSummary,
} from "./buffering-quality-types";

export type * from "./buffering-quality-types";

/** Stall time this early in a plan is startup, not a slow link. */
const STARTUP_GRACE_MS = 10_000;
/** A stall shorter than this is ordinary segment-boundary buffering. */
const STALL_MIN_MS = 1_000;
/**
 * A wait that begins with more than this much media buffered past the playhead
 * is the decoder or renderer catching up, not the network. It becomes a stall
 * once the buffer falls to this.
 */
export const RENDERER_WAIT_BUFFER_MS = 1_000;
/**
 * A wait, a start's included, that comes to hold more than this much media
 * past the playhead is the renderer's from then on, however it began: the
 * network delivered, so the stall so far ends uncounted and the wait stays the
 * renderer's until playback runs.
 */
export const RENDERER_WAIT_AMPLE_MS = 5_000;
/**
 * A wait in which none of the plan's media requests has received data for
 * this long is an outage: the link is silent, not slow. It is counted from
 * the last byte any of them received, so a wait that begins on a link
 * already silent this long is an outage at once. This is for progressive
 * reads, which the server streams as fast as the link allows.
 */
export const OUTAGE_NO_DATA_MS = 4_000;
/**
 * The same for segmented delivery (HLS transcode or remux segments). The
 * server holds a segment request silent while it produces the segment, up to
 * 12 s for a transcode, so a shorter silence is a slow transcode: a stall,
 * which lowering fixes.
 */
export const OUTAGE_NO_DATA_SEGMENTED_MS = 15_000;
/** This many counted stalls within the window means the link cannot keep up. */
const STALLS_BEFORE_TRIGGER = 2;
const STALL_WINDOW_MS = 120_000;
/** One stall this long is enough on its own. */
const LONG_STALL_MS = 6_000;
/** How long the prompt and the undo pill stay up, in wall-clock time. */
export const BUFFERING_PILL_MS = 15_000;
/** A prompt that timed out is offered again no sooner than this after it first showed. */
const REOFFER_MIN_MS = 120_000;
/** How long a plan that could not go lower is left alone. */
const NO_LOWER_COOLDOWN_MS = 300_000;
/** A lowering whose plan was never adopted is watched again after this. */
const REARM_AFTER_UNADOPTED_MS = 60_000;
/** The contract's floor for `bandwidth_estimate_kbps`. */
const MIN_ESTIMATE_KBPS = 100;

/** The constants the shared vectors pin, by their names there. */
export const BUFFERING_QUALITY_CONSTANTS = {
  STARTUP_GRACE_MS,
  STALL_MIN_MS,
  RENDERER_WAIT_BUFFER_MS,
  STALLS_BEFORE_TRIGGER,
  STALL_WINDOW_MS,
  LONG_STALL_MS,
  PROMPT_SECONDS: BUFFERING_PILL_MS / 1000,
  REOFFER_MIN_MS,
  NO_LOWER_COOLDOWN_MS,
  REARM_AFTER_UNADOPTED_MS,
  STEP_DOWN_RATIO: 0.7,
  MIN_ESTIMATE_KBPS,
} as const;

/** What the pills and notices say, the same on every client. */
export const BUFFERING_QUALITY_COPY = {
  prompt_title: "Playback keeps buffering",
  prompt_lower: "Lower quality",
  prompt_keep: "Keep quality",
  undo_caption: "Quality lowered to stop buffering",
  undo_action: "Restore quality",
  lowered: "Quality lowered to stop buffering",
  cant_lower: "Can't lower quality for this video",
} as const;

/**
 * The estimate Auto replans with after `deliveredKbps` stalled, or undefined
 * when it would fall below the contract's floor. Auto plans for 80% of an
 * estimate, so the next stream targets about 56% of the one that stalled. The
 * integer arithmetic is exact: multiplying by 0.7 can land one kbps low.
 */
function stepDownEstimateKbps(deliveredKbps: number): number | undefined {
  if (!Number.isFinite(deliveredKbps)) return undefined;
  const estimate = Math.floor((deliveredKbps * 7) / 10);
  return estimate >= MIN_ESTIMATE_KBPS ? estimate : undefined;
}

function adoptionOf(plan: BufferingPlan | undefined) {
  return plan && (plan.adoption_id ?? plan.plan_id);
}

export class BufferingQualityController {
  /** The pill on screen, if any. */
  pill: BufferingPill | null = null;
  /** What is left of the pill's timer; frozen while paused or obscured. */
  remainingMs = BUFFERING_PILL_MS;
  private plan?: BufferingPlan;
  private summary?: BufferingSummary;
  /**
   * When the plan's startup grace ends; set by its first frame or first wait.
   * A plan that continues the attempt's start keeps the one it had.
   */
  private graceEndsAt?: number;
  /**
   * When the stall in progress started counting toward a trigger. A prompt
   * timeout or a rearm restarts it, so it can be later than `stallBegan`.
   */
  private stall?: number;
  /** When the stall in progress began, for `plan_summary`. */
  private stallBegan?: number;
  private waiting = false;
  private playing = false;
  private paused = false;
  /** Media requests are failing or the device is offline: the reconnect path's stall. */
  private outage = false;
  /** When the last outage ended. */
  private outageEndedAt = -Infinity;
  /** When each counted stall in the window ended. */
  private stalls: number[] = [];
  /** When the last long stall ended; it qualifies on its own for a window. */
  private longStallEnded?: number;
  /** The viewer kept or restored this title's quality; cleared by a quality change. */
  private answered = false;
  private cantLowerShown = false;
  private blockedUntil = 0;
  /** When the current prompt first appeared on screen; it may wait behind another pill first. */
  private promptShownAt?: number;
  /** Only a timed-out prompt holds the next one off; a quality change ends the wait. */
  private reofferUntil = 0;
  /** The replan this controller sent, until its plan is adopted or it rearms. */
  private pending?: {
    at: number;
    bitrate: number;
    undo: boolean;
    restore?: boolean;
    /** The kept estimate before this lowering, restored if it is never sent. */
    keptBefore?: number;
  };
  private lastTick: number;
  /** A pill waiting behind a higher-priority one, with its timer frozen. */
  private deferredPill: BufferingPill | null = null;
  private obscured = false;
  /**
   * The viewer picked a quality that is not adopted yet. Its pill is gone and
   * nothing prompts or lowers meanwhile, but the answer, history and kept
   * estimate wait for the pick's adoption (`quality_changed`), and a lowering
   * adopted meanwhile is not announced.
   */
  private pickPending = false;
  /**
   * The quality of the last plan adopted. It outlives `stop`, so a plan the
   * page adopts again after the back-forward cache is no quality change.
   */
  private lastQuality?: string;
  /** In a Watch Party room: stalls are tracked and reported, but nothing acts. */
  private suspended = false;
  /** The latest Auto lowering's step-down estimate, for this playback attempt. */
  private keptEstimate?: number;
  /**
   * A plan of this playback attempt has played. Until one has, a wait may be
   * a stream that cannot start for another reason, so lowering needs
   * evidence that the plan's media arrived too slowly.
   */
  private attemptPlayed = false;

  constructor(
    public mode: BufferingMode,
    private emit: (output: BufferingOutput, at: number) => void,
    now: number,
    private onSummary?: (plan: BufferingPlan, summary: BufferingSummary) => void,
    private onKeptEstimate?: (kbps: number | undefined) => void,
    /**
     * The current plan's measured throughput, or undefined where the player
     * cannot measure it.
     */
    private measuredKbps?: () => number | undefined,
  ) {
    this.lastTick = now;
  }

  /**
   * A wait for media data is in progress: one this controller times as a
   * stall unless an outage holds it.
   */
  get waitingForData(): boolean {
    return this.waiting;
  }

  /**
   * When the visible pill's timer runs out, or null while it is frozen. It
   * stays fixed while the timer runs, so a view can count down on its own.
   */
  get pillDeadline(): number | null {
    return this.pill && !this.paused ? this.lastTick + this.remainingMs : null;
  }

  /** A higher-priority pill (intro, segment skip, Next Up) appeared or went away. */
  setObscured(value: boolean, now: number) {
    this.tick(now);
    this.obscured = value;
    if (value && this.pill) {
      this.deferredPill = this.pill;
      this.pill = null;
    } else if (!value && this.deferredPill) this.show(this.deferredPill, now, false);
  }

  /**
   * Joining or leaving a Watch Party room. Inside one the controller does not
   * act: no trigger, pill or notice, and summaries report `none`. Leaving
   * starts fresh stall history with the grace over. Neither is a plan boundary.
   */
  setSuspended(value: boolean, now: number) {
    this.tick(now);
    if (value === this.suspended) return;
    this.suspended = value;
    if (value) {
      this.pill = null;
      this.deferredPill = null;
      this.pending = undefined;
      // A room start is a new start; it does not repeat a lowering.
      this.keep(undefined);
    } else {
      this.graceEndsAt = Math.min(this.graceEndsAt ?? now, now);
      this.resetHistory(now);
    }
  }

  /**
   * The client's own start deadline passed before the plan's first frame, a
   * replacement's included. On Auto in the "always" mode that lowers as a long
   * stall would, with an undo, and answers true; the start then fails only
   * when there is nothing lower, including a plan a lowering just found could
   * go no lower. A picked quality, the "ask" and "never" modes, a Keep or
   * Restore answer for the title, a Watch Party room and a pending lowering
   * or pick leave the failure to the usual recovery. So does an outage in
   * progress or one that ended less than LONG_STALL_MS before: the start
   * never had a fair run on a working network. So does a start whose media
   * did not arrive too slowly (see mediaArrivedSlowly).
   */
  startDeadlinePassed(now: number): boolean {
    this.tick(now, false);
    if (
      this.plan?.quality !== "auto" ||
      !this.mediaArrivedSlowly() ||
      this.mode !== "always" ||
      this.answered ||
      this.suspended ||
      this.pending ||
      this.pickPending ||
      this.outage ||
      now - this.outageEndedAt < LONG_STALL_MS ||
      now < this.blockedUntil ||
      !this.canLower()
    )
      return false;
    this.pill = null;
    this.deferredPill = null;
    this.lower(now, true);
    return true;
  }

  /** The player stops: report the plan that was playing. */
  stop(now: number) {
    this.tick(now, false);
    this.flush(now);
    this.plan = undefined;
    this.summary = undefined;
  }

  event(event: BufferingEvent, now: number) {
    this.tick(now);
    switch (event.type) {
      case "pill_obscured":
        this.setObscured(true, now);
        break;
      case "pill_unobscured":
        this.setObscured(false, now);
        break;
      case "plan_adopted":
        if (event.plan)
          this.adopt(event.plan, event.controllerInitiated, event.continuesStart, now);
        break;
      case "play":
        this.paused = false;
        break;
      case "playing":
        this.paused = false;
        this.playing = true;
        this.graceEndsAt ??= now + STARTUP_GRACE_MS;
        // The attempt's first frame ends its start's wait, which counts
        // toward a trigger only with the evidence it needed while it lasted.
        this.finishStall(now, this.stallsCount());
        this.attemptPlayed = true;
        this.waiting = false;
        this.check(now);
        break;
      case "waiting":
        this.playing = false;
        if (event.cause || this.paused) {
          this.endStallUncounted(now);
          this.waiting = false;
          break;
        }
        this.waiting = true;
        this.graceEndsAt ??= now + STARTUP_GRACE_MS;
        // A stall that begins in the grace counts from its end.
        if (!this.outage && this.stall === undefined) this.beginStall(now);
        break;
      case "pause":
        this.paused = true;
        this.playing = false;
        this.endStallUncounted(now);
        this.waiting = false;
        break;
      case "seek":
        // A seek within the buffer keeps playing; one that runs out of data
        // reports a `waiting` with its cause.
        this.endStallUncounted(now);
        this.waiting = false;
        break;
      case "reload":
        this.endStallUncounted(now);
        this.waiting = false;
        this.playing = false;
        break;
      case "transport_error_start":
        // A stall that overlaps an outage is the outage's.
        this.outage = true;
        this.clearStall();
        break;
      case "transport_error_end":
        this.outage = false;
        this.outageEndedAt = now;
        if (this.waiting) this.beginStall(now);
        break;
      case "pick_sent":
        this.pill = null;
        this.deferredPill = null;
        this.pickPending = true;
        break;
      case "pick_refused":
        this.pickPending = false;
        // A long stall the pick held off is evaluated at once.
        if (this.longStallInProgress(now)) this.check(now);
        break;
      case "quality_changed":
        this.pickPending = false;
        this.keep(undefined);
        this.answered = false;
        this.reofferUntil = 0;
        this.pill = null;
        this.deferredPill = null;
        this.pending = undefined;
        this.resetHistory(now);
        break;
      case "select_lower":
        if (this.pill === "prompt") {
          this.pill = null;
          this.lower(now, false);
        }
        break;
      case "select_keep":
        if (this.pill === "prompt") {
          this.answered = true;
          this.pill = null;
          this.setAction("kept");
        }
        break;
      case "back": {
        // Back answers a prompt but only hides an undo pill, which ends its
        // suppression the same way its timer does.
        const undo = this.pill === "undo";
        if (this.pill === "prompt") {
          this.answered = true;
          this.setAction("kept");
        }
        this.pill = null;
        if (undo && this.longStallInProgress(now)) this.check(now);
        break;
      }
      case "select_restore":
        if (this.pill === "undo" && this.plan) {
          this.pill = null;
          this.answered = true;
          this.keep(undefined);
          this.setAction("restored");
          this.pending = { at: now, bitrate: this.plan.delivered_kbps, undo: false, restore: true };
          this.emit(
            {
              type: "replan",
              body: {
                operation: "quality_change",
                quality_preference: "auto",
                learned_limit: "clear",
              },
            },
            now,
          );
        }
        break;
      case "replan_failed":
        // The rearm is timed from the request, whether it failed or was never
        // adopted. A lowering that was never sent is not kept.
        if (event.unsent && this.pending && !this.pending.restore)
          this.keep(this.pending.keptBefore);
        break;
      case "new_attempt":
        this.keep(undefined);
        this.attemptPlayed = false;
        break;
      case "client_replan":
        if (this.plan && event.operation) {
          const auto = this.plan.quality === "auto";
          const body: ClientReplan = {
            operation: event.operation,
            quality_preference: this.plan.quality,
            ...(auto && this.keptEstimate !== undefined
              ? { bandwidth_estimate_kbps: this.keptEstimate }
              : {}),
          };
          this.emit({ type: "replan", body }, now);
        }
        break;
    }
  }

  /** Advances the clock: runs the pill timer, the rearm and long-stall checks. */
  tick(now: number, evaluate = true) {
    const elapsed = Math.max(0, now - this.lastTick);
    this.lastTick = now;
    if (this.summary) {
      this.summary.buffering_mode = this.mode;
      if (this.playing && !this.paused && !this.waiting) this.summary.played_ms += elapsed;
    }
    if (this.pill && !this.paused) {
      this.remainingMs -= elapsed;
      if (this.remainingMs <= 0) {
        // An undo pill that runs out leaves the lower plan playing; a prompt
        // that runs out is not an answer, so it starts the history over and
        // holds the next prompt off.
        if (this.pill === "prompt") {
          this.setAction("timed_out");
          this.resetHistory(now);
          this.reofferUntil = (this.promptShownAt ?? now) + REOFFER_MIN_MS;
        }
        this.pill = null;
      }
    }
    if (this.pending && now - this.pending.at >= REARM_AFTER_UNADOPTED_MS) {
      this.pending = undefined;
      this.resetHistory(now);
    }
    // A stall that reached the long-stall time is evaluated on every tick, so a
    // suppression that ends while it lasts evaluates it at once. One that ends
    // during a shorter wait, or while playing, waits for the next evaluation.
    if (evaluate && this.longStallInProgress(now)) this.check(now);
  }

  private longStallInProgress(now: number) {
    return this.waiting && this.stall !== undefined && now - this.stall >= LONG_STALL_MS;
  }

  private adopt(
    plan: BufferingPlan,
    controllerInitiated: boolean | undefined,
    continuesStart: boolean | undefined,
    now: number,
  ) {
    // An in-place update of the same adoption keeps its history.
    if (adoptionOf(plan) === adoptionOf(this.plan)) return;
    const previousQuality = this.lastQuality;
    this.lastQuality = plan.quality;
    this.flush(now);
    const pending = controllerInitiated === false ? undefined : this.pending;
    this.pending = undefined;
    this.plan = plan;
    // A quality change the controller did not make clears the answer and the
    // kept estimate.
    if (previousQuality !== plan.quality && !pending) {
      this.answered = false;
      this.keep(undefined);
    }
    this.summary = {
      played_ms: 0,
      stall_count: 0,
      stall_total_ms: 0,
      stall_longest_ms: 0,
      ...(plan.delivered_kbps > 0 ? { delivered_kbps: plan.delivered_kbps } : {}),
      buffering_mode: this.mode,
      buffering_action: "none",
    };
    this.pill = null;
    this.deferredPill = null;
    this.waiting = false;
    this.playing = false;
    if (!continuesStart) this.graceEndsAt = undefined;
    this.clearStall();
    this.stalls = [];
    this.longStallEnded = undefined;
    this.blockedUntil = 0;
    if (!pending || pending.restore) return;
    // A lowering the viewer's pending pick is about to replace shows nothing,
    // but its action and cooldown stand.
    if (
      plan.delivered_kbps >= pending.bitrate ||
      plan.degradation_warnings?.includes("quality_reduction_unavailable")
    ) {
      if (this.pickPending) this.setAction("cant_lower");
      else this.showCantLower(now, false);
      this.blockedUntil = now + NO_LOWER_COOLDOWN_MS;
    } else if (this.pickPending) return;
    else if (pending.undo) this.show("undo", now);
    else this.emit({ type: "notice", key: "lowered" }, now);
  }

  private show(pill: BufferingPill, now: number, resetTimer = true) {
    if (resetTimer) this.remainingMs = BUFFERING_PILL_MS;
    if (this.obscured) {
      this.deferredPill = pill;
      return;
    }
    this.deferredPill = null;
    this.pill = pill;
    if (pill === "prompt") this.promptShownAt ??= now;
    this.emit({ type: pill === "prompt" ? "prompt_shown" : "undo_shown" }, now);
  }

  private keep(kbps: number | undefined) {
    if (kbps === this.keptEstimate) return;
    this.keptEstimate = kbps;
    this.onKeptEstimate?.(kbps);
  }

  private setAction(action: BufferingAction) {
    if (this.summary) this.summary.buffering_action = action;
  }

  private beginStall(now: number) {
    this.stall = Math.max(now, this.graceEndsAt ?? now);
    this.stallBegan = this.stall;
  }

  private clearStall() {
    this.stall = undefined;
    this.stallBegan = undefined;
  }

  /**
   * A pause, seek or reload ends a stall without counting it; its time so far
   * still counts in the summary's total and longest.
   */
  private endStallUncounted(now: number) {
    if (this.stallBegan !== undefined && this.summary) {
      const duration = Math.max(0, now - this.stallBegan);
      this.summary.stall_total_ms += duration;
      this.summary.stall_longest_ms = Math.max(this.summary.stall_longest_ms, duration);
    }
    this.clearStall();
  }

  /** Starts the stall history over; a stall in progress is timed again from now. */
  private resetHistory(now: number) {
    this.stalls = [];
    this.longStallEnded = undefined;
    this.stall = this.waiting && !this.outage ? Math.max(now, this.graceEndsAt ?? now) : undefined;
  }

  /** Ends the stall in progress; one that does not `count` stays out of the trigger history. */
  private finishStall(now: number, count = true) {
    if (this.stall !== undefined && count) {
      const counted = Math.max(0, now - this.stall);
      if (counted >= STALL_MIN_MS) this.stalls.push(now);
      if (counted >= LONG_STALL_MS) this.longStallEnded = now;
    }
    if (this.stallBegan !== undefined && this.summary) {
      const duration = Math.max(0, now - this.stallBegan);
      this.summary.stall_total_ms += duration;
      this.summary.stall_longest_ms = Math.max(this.summary.stall_longest_ms, duration);
      if (duration >= STALL_MIN_MS) this.summary.stall_count++;
    }
    this.clearStall();
  }

  private flush(now: number) {
    this.finishStall(now);
    if (this.plan && this.summary) this.onSummary?.(this.plan, { ...this.summary });
  }

  /**
   * The web's lowerQualityOption over the plan's own menu: the highest rung
   * below the lower of the named rung's bitrate and the delivered bitrate. A
   * cap can encode below the rung the plan names, and the step must still be
   * below what plays.
   */
  private lowerRung() {
    const plan = this.plan;
    if (!plan) return undefined;
    const named = plan.available_qualities.find(
      (q) => plan.active_quality !== undefined && q.label === plan.active_quality,
    )?.bitrate_kbps;
    const delivered = plan.delivered_kbps > 0 ? plan.delivered_kbps : Infinity;
    const ceiling =
      named !== undefined && named > 0 ? Math.min(named, delivered) : plan.delivered_kbps;
    let best: BufferingPlan["available_qualities"][number] | undefined;
    for (const rung of plan.available_qualities) {
      const kbps = rung.bitrate_kbps ?? 0;
      if (kbps > 0 && kbps < ceiling && kbps > (best?.bitrate_kbps ?? 0)) best = rung;
    }
    return best;
  }

  /**
   * The evidence a lowering needs before the attempt has played, and a start
   * deadline's always: the plan's media arrived more slowly than it plays.
   * Without a measurement, or with one at the delivered bitrate or above, the
   * link may be silent, or the stream failing for another reason.
   */
  private mediaArrivedSlowly() {
    const kbps = this.measuredKbps?.();
    return kbps !== undefined && this.plan !== undefined && kbps < this.plan.delivered_kbps;
  }

  /** Whether stalls count toward a trigger: once the attempt has played, or on that evidence. */
  private stallsCount() {
    return this.attemptPlayed || this.mediaArrivedSlowly();
  }

  /** Whether this plan has a lower rung and, on Auto, a usable step-down estimate. */
  private canLower() {
    const plan = this.plan;
    if (!plan || !this.lowerRung()) return false;
    return plan.quality !== "auto" || stepDownEstimateKbps(plan.delivered_kbps) !== undefined;
  }

  private showCantLower(now: number, oncePerTitle = true) {
    this.setAction("cant_lower");
    if (oncePerTitle && this.cantLowerShown) return;
    this.cantLowerShown = true;
    this.emit({ type: "notice", key: "cant_lower" }, now);
  }

  private triggered(now: number) {
    this.stalls = this.stalls.filter((at) => now - at < STALL_WINDOW_MS);
    return (
      this.stalls.length >= STALLS_BEFORE_TRIGGER ||
      (this.stall !== undefined && now - this.stall >= LONG_STALL_MS) ||
      (this.longStallEnded !== undefined && now - this.longStallEnded < STALL_WINDOW_MS)
    );
  }

  private check(now: number) {
    // Suppressions leave the history in place for a later evaluation.
    if (
      !this.plan ||
      this.suspended ||
      this.paused ||
      this.outage ||
      this.answered ||
      this.pickPending ||
      this.pending ||
      this.pill ||
      this.deferredPill ||
      now < this.blockedUntil
    )
      return;
    if (!this.triggered(now) || !this.stallsCount()) return;
    if (this.mode === "never") {
      this.setAction("ignored");
      return;
    }
    if (!this.canLower()) {
      this.showCantLower(now);
      this.blockedUntil = now + NO_LOWER_COOLDOWN_MS;
      return;
    }
    if (this.mode === "always" && this.plan.quality === "auto") this.lower(now, true);
    else if (now >= this.reofferUntil) {
      this.promptShownAt = undefined;
      this.show("prompt", now);
    }
  }

  private lower(now: number, undo: boolean) {
    const plan = this.plan;
    const rung = this.lowerRung();
    const estimate = plan && stepDownEstimateKbps(plan.delivered_kbps);
    if (!plan || !rung || (plan.quality === "auto" && estimate === undefined)) {
      this.showCantLower(now);
      return;
    }
    this.pending = { at: now, bitrate: plan.delivered_kbps, undo, keptBefore: this.keptEstimate };
    this.setAction(undo ? "lowered" : "accepted");
    // Kept from the moment the lowering is sent; the next one replaces it.
    if (plan.quality === "auto") this.keep(estimate);
    // A lowered Auto teaches the server a limit; a picked quality steps down
    // one rung for this playback only.
    const body: BufferingReplan =
      plan.quality === "auto"
        ? {
            operation: "quality_change",
            quality_preference: "auto",
            bandwidth_estimate_kbps: estimate,
            bandwidth_estimate_source: "observed_rebuffer",
          }
        : { operation: "quality_change", quality_preference: rung.label };
    this.emit({ type: "replan", body }, now);
  }
}
