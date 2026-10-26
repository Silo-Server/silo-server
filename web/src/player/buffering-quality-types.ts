/**
 * The types the buffering-quality controller (./buffering-quality.ts) and its
 * adapter exchange: the plan it sees, the events it takes, the replans and
 * pills it emits, and the plan_summary fields it fills in.
 */
export type BufferingMode = "never" | "ask" | "always";

/** The plan as the controller sees it: one adoption of a server plan. */
export interface BufferingPlan {
  plan_id: string;
  /** Local adoption identity; server plan IDs can repeat across replans. */
  adoption_id?: string;
  /** `effective_recipe.bitrate_kbps`, else `source.bitrate_kbps`; 0 when unknown. */
  delivered_kbps: number;
  /** The active quality preference: "auto" or a rung label. */
  quality: string;
  /** The `available_qualities` entry the plan delivers, when the server names one. */
  active_quality?: string;
  available_qualities: { label: string; bitrate_kbps?: number; preserves_source?: boolean }[];
  degradation_warnings?: string[];
}

/** A lowering, or Restore quality, which the adapter sends as a quality change. */
export interface BufferingReplan {
  operation: "quality_change";
  quality_preference: string;
  bandwidth_estimate_kbps?: number;
  bandwidth_estimate_source?: "observed_rebuffer";
  /**
   * What the stalled plan's media arrived at, sent with the estimate. The
   * player adds it when it can measure it; the controller never does.
   */
  observed_throughput_kbps?: number;
  learned_limit?: "clear";
}

/** Replans the client sends for its own reasons rather than the controller's. */
export type ClientReplanOperation =
  | "track_change"
  | "seek_reanchor"
  | "seek_failure_recovery"
  | "failure_recovery"
  | "output_change";

/** A client replan as the vectors describe it: the kept estimate, without a source. */
export interface ClientReplan {
  operation: ClientReplanOperation;
  quality_preference: string;
  bandwidth_estimate_kbps?: number;
}

export type BufferingPill = "prompt" | "undo";
export type BufferingNotice = "lowered" | "cant_lower";

export type BufferingOutput =
  | { type: "replan"; body: BufferingReplan | ClientReplan }
  | { type: "prompt_shown" | "undo_shown" }
  | { type: "notice"; key: BufferingNotice };

/** What the viewer answered on a pill. */
export type BufferingAnswer = "select_lower" | "select_keep" | "select_restore" | "back";

export interface BufferingEvent {
  type:
    | BufferingAnswer
    | "plan_adopted"
    | "play"
    | "playing"
    | "waiting"
    | "pause"
    | "seek"
    | "reload"
    | "transport_error_start"
    | "transport_error_end"
    | "replan_failed"
    | "quality_changed"
    | "pick_sent"
    | "pick_refused"
    | "new_attempt"
    | "client_replan"
    | "pill_obscured"
    | "pill_unobscured";
  plan?: BufferingPlan;
  /** The operation of a `client_replan`. */
  operation?: ClientReplanOperation;
  /**
   * Why a `waiting` began, when it is not the network's: the player itself
   * caused it, or it began with media buffered (a renderer wait).
   */
  cause?: "seek" | "pause" | "reload" | "renderer";
  /** Whether a `plan_adopted` answers this controller's own replan. */
  controllerInitiated?: boolean;
  /**
   * On `plan_adopted`: the plan replaces one of the same attempt before any
   * of them played, so it continues the attempt's start under its grace.
   */
  continuesStart?: boolean;
  /**
   * On `replan_failed`: the player refused the replan before sending it, so
   * its estimate is not kept. It still rearms like a failed replan.
   */
  unsent?: boolean;
}

/** The last action on a plan, as `plan_summary` reports it. */
export type BufferingAction =
  | "none"
  | "ignored"
  | "lowered"
  | "restored"
  | "accepted"
  | "kept"
  | "timed_out"
  | "cant_lower";

export interface BufferingSummary {
  played_ms: number;
  stall_count: number;
  stall_total_ms: number;
  stall_longest_ms: number;
  /** Omitted when the plan has no delivered bitrate. */
  delivered_kbps?: number;
  buffering_mode: BufferingMode;
  buffering_action: BufferingAction;
  /**
   * The rate the plan's media arrived at while a request was receiving. The
   * player adds it when it can measure it; the controller never does.
   */
  throughput_kbps?: number;
}
