import {
  FEATURE_DEFAULT_AUDIO_RECONCILE_RESPONSE_V3,
  FEATURE_DEFERRED_TRACK_INVENTORY_V3,
  FEATURE_INVENTORY_UPDATED_V3,
  FEATURE_PLAN_INVALIDATED_V3,
  FEATURE_PLAYBACK_PLAN_V3,
  FEATURE_SOURCE_COMMITTED_V3,
  PROTOCOL_V3,
  type ClientCodecCapabilitiesV3,
  type ClientPlaybackContextV3,
  type FailureV3,
  type PlanV3,
  type ProgressPersistenceV3,
  type ReplanOperationV3,
  type ReplanRequestV3,
  type SelectedTracksV3,
  type StartRequestV3,
  type TrackIdentityV3,
} from "./protocol-v3";

/** Contract bound on `position_seconds` and `start_position`. */
const MAX_POSITION_SECONDS = 31_536_000;

/**
 * What every v3 surface in this app can do.
 *
 * Anything beyond it is named by the caller through `extraClientFeatures`,
 * because a feature token is a promise the server holds the client to and then
 * enforces destructively — a session that advertises the plan-invalidation
 * command and then rejects it gets stopped. The video player and the audiobook
 * player share these builders but do not execute the same realtime commands, so
 * neither may inherit the other's promises.
 */
const BASE_CLIENT_FEATURES_V3 = [FEATURE_PLAYBACK_PLAN_V3];

/**
 * The features the video watch page adds: it executes the realtime
 * `plan_invalidated` command (see `VideoPlayer`) and replans off the plan the
 * server names, follows the realtime `source_committed` event to re-key its
 * version menu to the streamed release, and folds the realtime
 * `inventory_updated` push into its track menus.
 *
 * `plan_invalidated_v1` stays on its own promise and is not sufficient for the
 * audio reconciliation: the server gates that withdrawal on
 * `default_audio_reconcile_response_v1`, which this surface also advertises
 * because it answers the withdrawal as a non-failure `track_change` and names
 * the reason on the replan.
 */
export const VIDEO_CLIENT_FEATURES_V3 = [
  FEATURE_PLAN_INVALIDATED_V3,
  "marker_thumbnails_v1",
  FEATURE_DEFAULT_AUDIO_RECONCILE_RESPONSE_V3,
  FEATURE_SOURCE_COMMITTED_V3,
  FEATURE_INVENTORY_UPDATED_V3,
  // A deferred plan marks tracks_pending and promises a follow-up: the menus
  // must stay in their loading state until inventory_updated (or the inventory
  // poll) replaces the provisional list. Without this token the server keeps
  // the synchronous path and never marks a plan pending.
  FEATURE_DEFERRED_TRACK_INVENTORY_V3,
];

/**
 * `client_features` is the contract's single advertisement location, and a
 * replan that sends it replaces the negotiated list — so start and replan build
 * it identically rather than letting a replan silently withdraw a promise.
 */
function clientFeaturesV3(extra: string[] | undefined): string[] {
  return [...BASE_CLIENT_FEATURES_V3, ...(extra ?? [])];
}

/**
 * Contract bounds on a replan's `failure`, in UTF-8 bytes. The server refuses a
 * replan that exceeds them, which would turn a recoverable failure into a stuck
 * player, so a long diagnostic is cut rather than sent whole.
 */
const FAILURE_BYTE_LIMITS = { classification: 64, message: 512, decoder_name: 128 } as const;

function truncateUtf8(value: string, maxBytes: number): string {
  const bytes = new TextEncoder().encode(value);
  if (bytes.length <= maxBytes) return value;
  // A character cut at the boundary decodes to U+FFFD; drop it.
  return new TextDecoder().decode(bytes.subarray(0, maxBytes)).replace(/\uFFFD$/, "");
}

/** A failure the server accepts: each field within its contract bound. */
export function boundFailureV3(failure: FailureV3): FailureV3 {
  const bounded: FailureV3 = {
    classification: truncateUtf8(failure.classification, FAILURE_BYTE_LIMITS.classification),
  };
  if (failure.message !== undefined) {
    bounded.message = truncateUtf8(failure.message, FAILURE_BYTE_LIMITS.message);
  }
  if (failure.decoder_name !== undefined) {
    bounded.decoder_name = truncateUtf8(failure.decoder_name, FAILURE_BYTE_LIMITS.decoder_name);
  }
  return bounded;
}

function clampPosition(seconds: number): number {
  if (!Number.isFinite(seconds) || seconds < 0) return 0;
  return Math.min(seconds, MAX_POSITION_SECONDS);
}

export interface ReplanOptions {
  bandwidthEstimateKbps?: number | null;
  bandwidthEstimateSource?: "probe" | "observed_rebuffer";
  /** The current plan's measured throughput; sent only with an `observed_rebuffer` estimate. */
  observedThroughputKbps?: number;
  learnedLimit?: "ignore" | "clear";
  operation: ReplanOperationV3;
  positionSeconds: number;
  /** Present only on `track_change`; `null` clears the subtitle selection. */
  audio?: TrackIdentityV3;
  subtitle?: TrackIdentityV3 | null;
  failure?: FailureV3;
  /**
   * The viewer's current Auto intent. Present on every replan so the server's
   * session flag matches the menu: a viewer who started on an explicit pick and
   * later re-armed Auto would otherwise be pinned to that pick, and a
   * dead-source recovery would refuse to rotate while the menu shows Auto.
   */
  autoFallback?: boolean;
  /**
   * The `reason` string of the `plan_invalidated` command this replan answers,
   * verbatim. Set only by the default-audio reconciliation path in
   * `invalidatePlan`; every other replan omits it, so the server can correlate
   * this replan with the decision it is holding without a heuristic.
   */
  answersPlanInvalidation?: string;
  /**
   * Client-only: the viewer's play/pause intent for this replan, when the
   * caller sampled it before the request. A seek replacement in particular is
   * issued while the outgoing transport may be torn down, so the session must
   * not re-read that transient pause. Never sent on the wire.
   */
  autoplayIntent?: boolean;
}

export interface StartRequestInput {
  /** Features this surface implements beyond {@link BASE_CLIENT_FEATURES_V3}. */
  extraClientFeatures?: string[];
  fileId: number;
  profileId: string;
  playbackAttemptId: string;
  qualityPreference: string;
  allowAlternateVersions?: boolean;
  position: number;
  /** Forces `start_position: 0` to be sent, which means "start over". */
  forceStartPosition: boolean;
  /** Omitted means the server owns durable item progress. */
  progressPersistence?: ProgressPersistenceV3;
  explicitAudioTrackIndex?: number | null;
  subtitleTrackIndex?: number | null;
  /** File-bound subtitle identity carried into a replacement start, so a
   * source transition keeps the selected language instead of falling back to
   * the new plan's default. */
  subtitleTrackID?: string | null;
  /** File-bound audio track identity carried from the current plan into a
   * replacement start (a version switch); remapped by family server-side. */
  carriedAudioTrackID?: string | null;
  /** How the requested file was chosen; `explicit` forbids silent server-side
   * version substitution. Omitted means the server owns the choice. */
  fileSelection?: "auto" | "explicit";
  /** Concrete offered candidate this start is binding to. */
  offeredVirtualURI?: string;
  /** When true, the server should force a re-link/re-query of the virtual file
   *  on this start attempt. Only sent when the viewer explicitly picks an
   *  unavailable version. */
  forceRelink?: boolean;
  bandwidthEstimateKbps?: number | null;
  bandwidthCapKbps?: number | null;
  bandwidthEstimateSource?: "probe" | "observed_rebuffer";
  learnedLimit?: "ignore" | "clear";
  clientCapabilities: ClientCodecCapabilitiesV3;
  clientPlaybackContext: ClientPlaybackContextV3;
}

/**
 * Builds a v3 start body.
 *
 * The client states what it *is* and what the viewer *asked for*; it does not
 * pick a file variant, encode recipe, or delivery. A user-configured bandwidth
 * ceiling remains declarative input for the server-owned ladder. `start_position`
 * is omitted rather than sent as zero when playback should simply resume, which
 * is what lets the server apply its own resume policy.
 */
export function buildStartRequestV3(input: StartRequestInput): StartRequestV3 {
  return {
    protocol_version: PROTOCOL_V3,
    client_features: clientFeaturesV3(input.extraClientFeatures),
    ...(input.bandwidthEstimateSource
      ? { bandwidth_estimate_source: input.bandwidthEstimateSource }
      : {}),
    ...(input.learnedLimit ? { learned_limit: input.learnedLimit } : {}),
    file_id: input.fileId,
    profile_id: input.profileId,
    playback_attempt_id: input.playbackAttemptId,
    quality_preference: input.qualityPreference,
    ...(input.allowAlternateVersions === false ? { allow_alternate_versions: false } : {}),
    // The web player renders ASS with its own typesetting engine, so it asks
    // the server to keep authored fidelity rather than flatten it.
    subtitle_fidelity_preference: "preserve",
    client_capabilities: input.clientCapabilities,
    client_playback_context: input.clientPlaybackContext,
    ...(input.forceStartPosition || input.position > 0
      ? { start_position: clampPosition(input.position) }
      : {}),
    ...(input.progressPersistence ? { progress_persistence: input.progressPersistence } : {}),
    ...(input.explicitAudioTrackIndex != null && input.explicitAudioTrackIndex >= 0
      ? { audio_track_index: input.explicitAudioTrackIndex }
      : {}),
    ...(input.carriedAudioTrackID ? { carried_audio_track_id: input.carriedAudioTrackID } : {}),
    ...(input.fileSelection ? { file_selection: input.fileSelection } : {}),
    ...(input.offeredVirtualURI ? { offered_virtual_uri: input.offeredVirtualURI } : {}),
    ...(input.forceRelink ? { force_relink: input.forceRelink } : {}),
    ...(input.subtitleTrackIndex != null && input.subtitleTrackIndex >= 0
      ? { subtitle_track_index: input.subtitleTrackIndex }
      : {}),
    ...(input.subtitleTrackID ? { subtitle_track_id: input.subtitleTrackID } : {}),
    ...(input.bandwidthEstimateKbps != null
      ? { bandwidth_estimate_kbps: input.bandwidthEstimateKbps }
      : {}),
    ...(input.bandwidthCapKbps != null ? { bandwidth_cap_kbps: input.bandwidthCapKbps } : {}),
  };
}

export interface ReplanRequestInput extends ReplanOptions {
  /** Features this surface implements beyond {@link BASE_CLIENT_FEATURES_V3}. */
  extraClientFeatures?: string[];
  plan: PlanV3;
  playbackAttemptId: string;
  replanRequestId: string;
  planAttemptId: string;
  qualityPreference: string;
  attemptedPlanKeys: string[];
  attemptCount: number;
  bandwidthCapKbps?: number | null;
  clientCapabilities: ClientCodecCapabilitiesV3;
  clientPlaybackContext: ClientPlaybackContextV3;
}

/**
 * Builds a v3 replan body.
 *
 * The selection echoes the plan's own `selected_tracks` and overrides only the
 * side that changed. That matters twice over: an absent subtitle identity means
 * "subtitles off", so an audio-only change has to resend the subtitle it is not
 * touching; and the seek operations are validated against the current plan's
 * tracks byte-for-byte, so they must never be rewritten into shorthand.
 */
export function buildReplanRequestV3(input: ReplanRequestInput): ReplanRequestV3 {
  const selectedTracks: SelectedTracksV3 = {};
  const nextAudio = input.audio ?? input.plan.selected_tracks.audio;
  if (nextAudio) {
    selectedTracks.audio = nextAudio;
  }
  const nextSubtitle =
    input.subtitle === undefined ? input.plan.selected_tracks.subtitle : input.subtitle;
  if (nextSubtitle) {
    selectedTracks.subtitle = nextSubtitle;
  }

  return {
    protocol_version: PROTOCOL_V3,
    client_features: clientFeaturesV3(input.extraClientFeatures),
    ...(input.bandwidthEstimateSource
      ? { bandwidth_estimate_source: input.bandwidthEstimateSource }
      : {}),
    ...(input.learnedLimit ? { learned_limit: input.learnedLimit } : {}),
    operation: input.operation,
    playback_attempt_id: input.playbackAttemptId,
    replan_request_id: input.replanRequestId,
    failed_plan_id: input.plan.plan_id,
    plan_attempt_id: input.planAttemptId,
    plan_attempt_key: input.plan.plan_attempt_key,
    attempted_plan_keys: input.attemptedPlanKeys,
    attempt_count: input.attemptCount,
    quality_preference: input.qualityPreference,
    position_seconds: clampPosition(input.positionSeconds),
    ...(input.autoFallback !== undefined ? { auto_fallback: input.autoFallback } : {}),
    // Omitted rather than sent empty: an absent field means "this replan is not
    // an answer to any withdrawal", which is what every non-reconciliation
    // replan actually is.
    ...(input.answersPlanInvalidation
      ? { answers_plan_invalidation: input.answersPlanInvalidation }
      : {}),
    selected_tracks: selectedTracks,
    client_capabilities: input.clientCapabilities,
    client_playback_context: input.clientPlaybackContext,
    ...(input.failure ? { failure: boundFailureV3(input.failure) } : {}),
    ...(input.bandwidthEstimateKbps != null
      ? { bandwidth_estimate_kbps: input.bandwidthEstimateKbps }
      : {}),
    ...(input.observedThroughputKbps !== undefined &&
    input.bandwidthEstimateSource === "observed_rebuffer"
      ? { observed_throughput_kbps: input.observedThroughputKbps }
      : {}),
    ...(input.bandwidthCapKbps != null ? { bandwidth_cap_kbps: input.bandwidthCapKbps } : {}),
  };
}

/**
 * Route events for a terminal start carry only the playback-attempt identity.
 * Plan-scoped fields become valid only after the server returned a plan; a
 * client-generated plan-attempt id on a plan-less terminal is intentionally
 * omitted so the server can authorize it against the persisted start attempt.
 */
export function routeEventPlanIdentityV3(
  plan: PlanV3 | null,
  sessionId: string | null,
  planAttemptId: string,
) {
  if (!plan) return {};
  return {
    sessionId,
    planId: plan.plan_id,
    planAttemptId,
    planAttemptKey: plan.plan_attempt_key,
  };
}
