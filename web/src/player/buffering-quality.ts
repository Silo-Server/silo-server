/** Shared buffering policy. The adapter owns media events and the wall clock. */
export type BufferingMode = "never" | "ask" | "always";
export interface BufferingPlan {
  plan_id: string;
  delivered_kbps: number;
  quality: string;
  available_qualities: { label: string; bitrate_kbps?: number; preserves_source?: boolean }[];
  degradation_warnings?: string[];
}
export interface BufferingReplan {
  operation: "quality_change";
  quality_preference: string;
  bandwidth_estimate_kbps?: number;
  bandwidth_estimate_source?: "observed_rebuffer";
  learned_limit?: "clear";
}
export type BufferingOutput =
  | { type: "replan"; body: BufferingReplan }
  | { type: "prompt_shown" | "undo_shown" }
  | { type: "notice"; key: "lowered" | "cant_lower" };
export interface BufferingEvent {
  type: string;
  plan?: BufferingPlan;
  cause?: string;
  controllerInitiated?: boolean;
}
export interface BufferingSummary {
  played_ms: number;
  stall_count: number;
  stall_total_ms: number;
  stall_longest_ms: number;
  delivered_kbps: number;
  buffering_mode: BufferingMode;
  buffering_action: string;
}

export class BufferingQualityController {
  pill: "prompt" | "undo" | null = null;
  remainingMs = 15_000;
  readonly summaries = new Map<string, BufferingSummary>();
  private plan?: BufferingPlan;
  private grace?: number;
  private stall?: number;
  private waiting = false;
  private playing = false;
  private paused = false;
  private outage = false;
  private stalls: number[] = [];
  private longEnded?: number;
  private answered = false;
  private cantNotified = false;
  private blockedUntil = 0;
  private offeredAt = -Infinity;
  private pending?: { at: number; bitrate: number; undo: boolean; restore?: boolean };
  private lastTick: number;
  private deferredPill: "prompt" | "undo" | null = null;
  private obscured = false;

  constructor(
    public mode: BufferingMode,
    private emit: (output: BufferingOutput, at: number) => void,
    now: number,
    private onSummary?: (plan: BufferingPlan, summary: BufferingSummary) => void,
  ) {
    this.lastTick = now;
  }

  setObscured(value: boolean, now: number) {
    this.obscured = value;
    if (!value && this.deferredPill) this.show(this.deferredPill, now);
  }
  private show(pill: "prompt" | "undo", now: number) {
    if (this.obscured) {
      this.deferredPill = pill;
      return;
    }
    this.deferredPill = null;
    this.pill = pill;
    this.remainingMs = 15_000;
    this.emit({ type: pill === "prompt" ? "prompt_shown" : "undo_shown" }, now);
  }
  private summary() {
    return this.plan && this.summaries.get(this.plan.plan_id);
  }
  private action(value: string) {
    const s = this.summary();
    if (s) s.buffering_action = value;
  }
  private resetHistory(now: number) {
    this.finishStall(now);
    this.stalls = [];
    this.longEnded = undefined;
    this.stall = this.waiting && !this.outage ? Math.max(now, this.grace ?? now) : undefined;
  }
  private finishStall(now: number) {
    if (this.stall === undefined) return;
    const duration = Math.max(0, now - this.stall);
    const summary = this.summary();
    if (summary) {
      summary.stall_total_ms += duration;
      summary.stall_longest_ms = Math.max(summary.stall_longest_ms, duration);
      if (duration >= 1_000) summary.stall_count++;
    }
    if (duration >= 1_000) this.stalls.push(now);
    if (duration >= 6_000) this.longEnded = now;
    this.stall = undefined;
  }
  private flush(now: number) {
    this.finishStall(now);
    const summary = this.summary();
    if (this.plan && summary) this.onSummary?.(this.plan, { ...summary });
  }
  stop(now: number) {
    this.tick(now, false);
    this.flush(now);
    this.plan = undefined;
  }

  event(event: BufferingEvent, now: number) {
    this.tick(now);
    switch (event.type) {
      case "plan_adopted": {
        if (!event.plan || event.plan.plan_id === this.plan?.plan_id) return;
        const previousQuality = this.plan?.quality;
        this.flush(now);
        const pending = event.controllerInitiated === false ? undefined : this.pending;
        this.pending = undefined;
        this.plan = event.plan;
        if (previousQuality !== this.plan.quality && !pending) this.answered = false;
        this.summaries.set(this.plan.plan_id, {
          played_ms: 0,
          stall_count: 0,
          stall_total_ms: 0,
          stall_longest_ms: 0,
          delivered_kbps: this.plan.delivered_kbps,
          buffering_mode: this.mode,
          buffering_action: "none",
        });
        this.pill = null;
        this.deferredPill = null;
        this.waiting = false;
        this.playing = false;
        this.grace = undefined;
        this.stall = undefined;
        this.stalls = [];
        this.longEnded = undefined;
        this.blockedUntil = 0;
        if (pending && !pending.restore) {
          if (
            this.plan.delivered_kbps >= pending.bitrate ||
            this.plan.degradation_warnings?.includes("quality_reduction_unavailable")
          ) {
            this.noticeCant(now, false);
            this.blockedUntil = now + 300_000;
          } else if (pending.undo) this.show("undo", now);
          else this.emit({ type: "notice", key: "lowered" }, now);
        }
        break;
      }
      case "play":
        this.paused = false;
        break;
      case "playing":
        this.paused = false;
        this.playing = true;
        this.grace ??= now + 10_000;
        this.finishStall(now);
        this.waiting = false;
        break;
      case "waiting":
        this.playing = false;
        if (event.cause || this.paused) {
          this.stall = undefined;
          this.waiting = false;
          break;
        }
        this.waiting = true;
        this.grace ??= now + 10_000;
        if (!this.outage) this.stall ??= Math.max(now, this.grace);
        break;
      case "pause":
        this.paused = true;
        this.playing = false;
        this.stall = undefined;
        this.waiting = false;
        break;
      case "seek":
      case "reload":
        this.stall = undefined;
        this.waiting = false;
        this.playing = false;
        break;
      case "transport_error_start":
        this.outage = true;
        this.stall = undefined;
        break;
      case "transport_error_end":
        this.outage = false;
        if (this.waiting) this.stall = Math.max(now, this.grace ?? now);
        break;
      case "quality_changed":
        this.answered = false;
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
          this.action("kept");
        }
        break;
      case "back":
        if (this.pill === "prompt") {
          this.answered = true;
          this.action("kept");
        }
        this.pill = null;
        break;
      case "select_restore":
        if (this.pill === "undo" && this.plan) {
          this.pill = null;
          this.answered = true;
          this.action("restored");
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
        break; // Rearm is timed from the request, even on failure.
    }
    if (event.type === "playing") this.check(now);
  }
  tick(now: number, evaluate = true) {
    const previousTick = this.lastTick;
    const elapsed = Math.max(0, now - this.lastTick);
    this.lastTick = now;
    const summary = this.summary();
    if (summary) summary.buffering_mode = this.mode;
    if (summary && this.playing && !this.paused && !this.waiting) summary.played_ms += elapsed;
    if (this.pill && !this.paused) {
      this.remainingMs -= elapsed;
      if (this.remainingMs <= 0) {
        if (this.pill === "prompt") {
          this.action("timed_out");
          this.resetHistory(now);
        }
        this.pill = null;
      }
    }
    if (this.pending && now - this.pending.at >= 60_000) {
      this.pending = undefined;
      this.resetHistory(now);
    }
    const suppressionEnded =
      (previousTick < this.blockedUntil && now >= this.blockedUntil) ||
      (previousTick < this.offeredAt + 120_000 && now >= this.offeredAt + 120_000);
    if (
      evaluate &&
      this.waiting &&
      ((this.stall !== undefined && now - this.stall >= 6_000) || suppressionEnded)
    )
      this.check(now);
  }
  private lowerRung() {
    if (!this.plan) return undefined;
    const active =
      this.plan.quality === "auto"
        ? this.plan.delivered_kbps
        : (this.plan.available_qualities.find((q) => q.label === this.plan?.quality)
            ?.bitrate_kbps ?? this.plan.delivered_kbps);
    return this.plan.available_qualities
      .filter((q) => (q.bitrate_kbps ?? 0) > 0 && q.bitrate_kbps! < active)
      .sort((a, b) => b.bitrate_kbps! - a.bitrate_kbps!)[0];
  }
  private noticeCant(now: number, once = true) {
    this.action("cant_lower");
    if (!once || !this.cantNotified) {
      this.cantNotified = true;
      this.emit({ type: "notice", key: "cant_lower" }, now);
    }
  }
  private check(now: number) {
    if (
      !this.plan ||
      this.paused ||
      this.outage ||
      this.answered ||
      this.pending ||
      this.pill ||
      this.deferredPill ||
      now < this.blockedUntil
    )
      return;
    this.stalls = this.stalls.filter((at) => now - at < 120_000);
    if (
      !(
        this.stalls.length >= 2 ||
        (this.stall !== undefined && now - this.stall >= 6_000) ||
        (this.longEnded !== undefined && now - this.longEnded < 120_000)
      )
    )
      return;
    if (this.mode === "never") {
      this.action("ignored");
      return;
    }
    if (!this.lowerRung()) {
      this.noticeCant(now);
      this.blockedUntil = now + 300_000;
      return;
    }
    if (this.mode === "always" && this.plan.quality === "auto") this.lower(now, true);
    else if (now - this.offeredAt >= 120_000) {
      this.offeredAt = now;
      this.show("prompt", now);
    }
  }
  private lower(now: number, undo: boolean) {
    if (!this.plan) return;
    const rung = this.lowerRung();
    const estimate = Math.floor(this.plan.delivered_kbps * 0.7);
    if (!rung || (this.plan.quality === "auto" && estimate < 100)) {
      this.noticeCant(now);
      return;
    }
    this.pending = { at: now, bitrate: this.plan.delivered_kbps, undo };
    this.action(undo ? "lowered" : "accepted");
    this.emit(
      {
        type: "replan",
        body:
          this.plan.quality === "auto"
            ? {
                operation: "quality_change",
                quality_preference: "auto",
                bandwidth_estimate_kbps: estimate,
                bandwidth_estimate_source: "observed_rebuffer",
              }
            : { operation: "quality_change", quality_preference: rung.label },
      },
      now,
    );
  }
}
