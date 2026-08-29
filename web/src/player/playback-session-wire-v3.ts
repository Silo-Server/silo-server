import {
  FEATURE_PLAN_INVALIDATED_V3,
  FEATURE_PLAYBACK_PLAN_V3,
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
 * server names.
 */
export const VIDEO_CLIENT_FEATURES_V3 = [FEATURE_PLAN_INVALIDATED_V3];

/**
 * `client_features` is the contract's single advertisement location, and a
 * replan that sends it replaces the negotiated list — so start and replan build
 * it identically rather than letting a replan silently withdraw a promise.
 */
function clientFeaturesV3(extra: string[] | undefined): string[] {
  return [...BASE_CLIENT_FEATURES_V3, ...(extra ?? [])];
}

function clampPosition(seconds: number): number {
  if (!Number.isFinite(seconds) || seconds < 0) return 0;
  return Math.min(seconds, MAX_POSITION_SECONDS);
}

export interface ReplanOptions {
  operation: ReplanOperationV3;
  positionSeconds: number;
  /** Present only on `track_change`; `null` clears the subtitle selection. */
  audio?: TrackIdentityV3;
  subtitle?: TrackIdentityV3 | null;
  failure?: FailureV3;
}

export interface StartRequestInput {
  /** Features this surface implements beyond {@link BASE_CLIENT_FEATURES_V3}. */
  extraClientFeatures?: string[];
  fileId: number;
  profileId: string;
  playbackAttemptId: string;
  qualityPreference: string;
  position: number;
  /** Forces `start_position: 0` to be sent, which means "start over". */
  forceStartPosition: boolean;
  /** Omitted means the server owns durable item progress. */
  progressPersistence?: ProgressPersistenceV3;
  explicitAudioTrackIndex?: number | null;
  subtitleTrackIndex?: number | null;
  metered: boolean;
  bandwidthEstimateKbps?: number | null;
  bandwidthCapKbps?: number | null;
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
    file_id: input.fileId,
    profile_id: input.profileId,
    playback_attempt_id: input.playbackAttemptId,
    quality_preference: input.qualityPreference,
    // The web player renders ASS with its own typesetting engine, so it asks
    // the server to keep authored fidelity rather than flatten it.
    subtitle_fidelity_preference: "preserve",
    metered: input.metered,
    client_capabilities: input.clientCapabilities,
    client_playback_context: input.clientPlaybackContext,
    ...(input.forceStartPosition || input.position > 0
      ? { start_position: clampPosition(input.position) }
      : {}),
    ...(input.progressPersistence ? { progress_persistence: input.progressPersistence } : {}),
    ...(input.explicitAudioTrackIndex != null && input.explicitAudioTrackIndex >= 0
      ? { audio_track_index: input.explicitAudioTrackIndex }
      : {}),
    ...(input.subtitleTrackIndex != null && input.subtitleTrackIndex >= 0
      ? { subtitle_track_index: input.subtitleTrackIndex }
      : {}),
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
  metered: boolean;
  bandwidthEstimateKbps?: number | null;
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
    metered: input.metered,
    selected_tracks: selectedTracks,
    client_capabilities: input.clientCapabilities,
    client_playback_context: input.clientPlaybackContext,
    ...(input.failure ? { failure: input.failure } : {}),
    ...(input.bandwidthEstimateKbps != null
      ? { bandwidth_estimate_kbps: input.bandwidthEstimateKbps }
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
