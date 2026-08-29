import type { SubtitleInventoryItemV3 } from "./protocol-v3";

export type PlaybackRealtimeMessageType = "command" | "event" | "hello" | "ack" | "result";

export type PlaybackCommandName =
  | "pause"
  | "unpause"
  | "play_pause"
  | "seek"
  | "set_volume"
  | "stop"
  | "terminate"
  | "display_message"
  | "server_restarting"
  | "server_shutting_down"
  | "play_media"
  | "set_audio_track"
  | "set_subtitle_track"
  | "plan_invalidated";

export type PlaybackRealtimeAckStatus = "accepted";
export type PlaybackRealtimeResultStatus = "completed" | "rejected";
export type PlaybackRealtimeEventName =
  | "chapter_thumbnail_ready"
  | "markers_updated"
  | "subtitle_ready"
  | "subtitle_translation_started"
  | "subtitle_translation_cues"
  | "subtitle_translation_completed"
  | "subtitle_translation_failed";

export interface PlaybackRealtimeCommandEnvelope {
  type: "command";
  command_id: string;
  session_id: string;
  name: PlaybackCommandName;
  reason?: string;
  issued_by?: {
    kind: string;
  };
  deadline_ms?: number;
  payload?: Record<string, unknown>;
}

/**
 * Payload of the `plan_invalidated` command: the server decided, after the plan
 * was already playing, that the route it names cannot serve this source.
 *
 * `plan_id` is the invalidated plan, not necessarily the one on screen — a
 * client that has already replanned past it has nothing left to do.
 */
export interface PlaybackPlanInvalidatedPayload {
  reason: string;
  plan_id: string;
}

export interface PlaybackRealtimeHelloEnvelope {
  type: "hello";
  session_id: string;
  client: {
    name: string;
    version: string;
  };
  capabilities: {
    commands: PlaybackCommandName[];
  };
}

export interface PlaybackChapterThumbnailReadyPayload {
  session_id: string;
  file_id: number;
  chapter_index: number;
  thumbnail_url: string;
  thumbnail_thumbhash?: string;
}

export interface PlaybackTimeRangePayload {
  start: number;
  end: number;
}

export interface PlaybackMarkersUpdatedPayload {
  session_id: string;
  file_id: number;
  intro?: PlaybackTimeRangePayload | null;
  credits?: PlaybackTimeRangePayload | null;
  recap?: PlaybackTimeRangePayload | null;
  preview?: PlaybackTimeRangePayload | null;
}

/**
 * Broadcast to every session watching a file when a newly generated subtitle
 * track (AI translation, later ASR) has been persisted, so players can refresh
 * their track list and pick it up without a manual reload.
 */
export interface PlaybackSubtitleReadyPayload {
  session_id: string;
  file_id: number;
  subtitle_id: number;
  language: string;
  label?: string;
  /**
   * The new track's server-assigned combined ordinal, identity and stream URL.
   * The client folds it in at that ordinal rather than deriving one. Absent
   * when the server could not resolve the file's inventory, in which case the
   * client refetches its plan instead.
   */
  track?: SubtitleInventoryItemV3;
}

/** One translated subtitle cue pushed during a live translation (media seconds). */
export interface PlaybackStreamCue {
  start: number;
  end: number;
  text: string;
}

export interface PlaybackSubtitleTranslationStartedPayload {
  session_id: string;
  file_id: number;
  job_id: number;
  track_key: string;
  language: string;
  label?: string;
  total_cues: number;
}

export interface PlaybackSubtitleTranslationCuesPayload {
  session_id: string;
  file_id: number;
  job_id: number;
  track_key: string;
  cues: PlaybackStreamCue[];
  done: number;
  total: number;
}

export interface PlaybackSubtitleTranslationCompletedPayload {
  session_id: string;
  file_id: number;
  job_id: number;
  track_key: string;
  subtitle_id: number;
  language: string;
  label?: string;
  /** See {@link PlaybackSubtitleReadyPayload.track}. */
  track?: SubtitleInventoryItemV3;
}

export interface PlaybackSubtitleTranslationFailedPayload {
  session_id: string;
  file_id: number;
  job_id: number;
  track_key: string;
  message?: string;
}

export interface PlaybackRealtimeEventEnvelopeBase {
  type: "event";
  session_id: string;
}

export type PlaybackRealtimeEventEnvelope =
  | (PlaybackRealtimeEventEnvelopeBase & {
      name: "chapter_thumbnail_ready";
      payload: PlaybackChapterThumbnailReadyPayload;
    })
  | (PlaybackRealtimeEventEnvelopeBase & {
      name: "markers_updated";
      payload: PlaybackMarkersUpdatedPayload;
    })
  | (PlaybackRealtimeEventEnvelopeBase & {
      name: "subtitle_ready";
      payload: PlaybackSubtitleReadyPayload;
    })
  | (PlaybackRealtimeEventEnvelopeBase & {
      name: "subtitle_translation_started";
      payload: PlaybackSubtitleTranslationStartedPayload;
    })
  | (PlaybackRealtimeEventEnvelopeBase & {
      name: "subtitle_translation_cues";
      payload: PlaybackSubtitleTranslationCuesPayload;
    })
  | (PlaybackRealtimeEventEnvelopeBase & {
      name: "subtitle_translation_completed";
      payload: PlaybackSubtitleTranslationCompletedPayload;
    })
  | (PlaybackRealtimeEventEnvelopeBase & {
      name: "subtitle_translation_failed";
      payload: PlaybackSubtitleTranslationFailedPayload;
    });

export interface PlaybackRealtimeAckEnvelope {
  type: "ack";
  command_id: string;
  session_id: string;
  status: PlaybackRealtimeAckStatus;
}

export interface PlaybackRealtimeResultEnvelope {
  type: "result";
  command_id: string;
  session_id: string;
  status: PlaybackRealtimeResultStatus;
  error?: string;
}

export const ALL_PLAYBACK_COMMANDS: PlaybackCommandName[] = [
  "pause",
  "unpause",
  "play_pause",
  "seek",
  "set_volume",
  "stop",
  "terminate",
  "display_message",
  "server_restarting",
  "server_shutting_down",
  "play_media",
  "set_audio_track",
  "set_subtitle_track",
  "plan_invalidated",
];

/** The commands every realtime surface in this app executes. */
export const SUPPORTED_PLAYBACK_COMMANDS: PlaybackCommandName[] = [
  "pause",
  "unpause",
  "play_pause",
  "seek",
  "set_volume",
  "stop",
  "terminate",
  "display_message",
  "server_restarting",
  "server_shutting_down",
];

/**
 * What the video player executes on top of the shared set. `plan_invalidated`
 * needs a replan the audiobook surface has no route ladder for, so the hello is
 * per-surface rather than one list both over-claim.
 */
export const VIDEO_PLAYBACK_COMMANDS: PlaybackCommandName[] = [
  ...SUPPORTED_PLAYBACK_COMMANDS,
  "plan_invalidated",
];

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isCommandName(value: unknown): value is PlaybackCommandName {
  return typeof value === "string" && ALL_PLAYBACK_COMMANDS.includes(value as PlaybackCommandName);
}

/**
 * Reads a `plan_invalidated` payload, or null when it is not well formed.
 *
 * Both fields are required: without `plan_id` the client cannot tell whether
 * the invalidated plan is still the one playing, and acting anyway would evict
 * a route the server never complained about.
 */
export function readPlanInvalidatedPayload(
  payload: Record<string, unknown> | undefined,
): PlaybackPlanInvalidatedPayload | null {
  if (!isRecord(payload)) return null;
  const { reason, plan_id: planId } = payload;
  if (typeof reason !== "string" || reason.trim() === "") return null;
  if (typeof planId !== "string" || planId.trim() === "") return null;
  return { reason, plan_id: planId };
}

function isChapterThumbnailReadyPayload(
  value: unknown,
): value is PlaybackChapterThumbnailReadyPayload {
  return (
    isRecord(value) &&
    typeof value.session_id === "string" &&
    typeof value.file_id === "number" &&
    typeof value.chapter_index === "number" &&
    typeof value.thumbnail_url === "string" &&
    (value.thumbnail_thumbhash === undefined || typeof value.thumbnail_thumbhash === "string")
  );
}

function isTimeRangePayload(value: unknown): value is PlaybackTimeRangePayload {
  return isRecord(value) && typeof value.start === "number" && typeof value.end === "number";
}

function isMarkersUpdatedPayload(value: unknown): value is PlaybackMarkersUpdatedPayload {
  const isOptionalRange = (range: unknown) =>
    range === undefined || range === null || isTimeRangePayload(range);
  return (
    isRecord(value) &&
    typeof value.session_id === "string" &&
    typeof value.file_id === "number" &&
    isOptionalRange(value.intro) &&
    isOptionalRange(value.credits) &&
    isOptionalRange(value.recap) &&
    isOptionalRange(value.preview)
  );
}

/** An optional string field is valid only when absent or actually a string. */
function isOptionalString(value: unknown): boolean {
  return value === undefined || typeof value === "string";
}

/**
 * Validates a pushed inventory entry.
 *
 * The combined ordinal is the whole point of the block — it is what lets the
 * client select the track without counting — so an entry missing it is worse
 * than no entry at all, and is rejected in favour of refetching the plan.
 */
function isSubtitleInventoryItem(value: unknown): value is SubtitleInventoryItemV3 {
  return (
    isRecord(value) &&
    typeof value.track_id === "string" &&
    typeof value.combined_index === "number" &&
    Number.isInteger(value.combined_index) &&
    value.combined_index >= 0 &&
    typeof value.source === "string" &&
    typeof value.forced === "boolean" &&
    typeof value.default === "boolean" &&
    typeof value.hearing_impaired === "boolean" &&
    (value.delivery === "sidecar" || value.delivery === "burn_in_only") &&
    isOptionalString(value.codec) &&
    isOptionalString(value.language) &&
    isOptionalString(value.label) &&
    isOptionalString(value.url) &&
    isOptionalString(value.font_bundle_url)
  );
}

function isOptionalSubtitleInventoryItem(value: unknown): boolean {
  return value === undefined || value === null || isSubtitleInventoryItem(value);
}

function isSubtitleReadyPayload(value: unknown): value is PlaybackSubtitleReadyPayload {
  return (
    isRecord(value) &&
    typeof value.session_id === "string" &&
    typeof value.file_id === "number" &&
    typeof value.subtitle_id === "number" &&
    typeof value.language === "string" &&
    isOptionalString(value.label) &&
    isOptionalSubtitleInventoryItem(value.track)
  );
}

function isStreamCue(value: unknown): value is PlaybackStreamCue {
  return (
    isRecord(value) &&
    typeof value.start === "number" &&
    typeof value.end === "number" &&
    typeof value.text === "string"
  );
}

function isTranslationStartedPayload(
  value: unknown,
): value is PlaybackSubtitleTranslationStartedPayload {
  return (
    isRecord(value) &&
    typeof value.session_id === "string" &&
    typeof value.file_id === "number" &&
    typeof value.job_id === "number" &&
    typeof value.track_key === "string" &&
    typeof value.language === "string" &&
    typeof value.total_cues === "number" &&
    isOptionalString(value.label)
  );
}

function isTranslationCuesPayload(value: unknown): value is PlaybackSubtitleTranslationCuesPayload {
  return (
    isRecord(value) &&
    typeof value.session_id === "string" &&
    typeof value.file_id === "number" &&
    typeof value.job_id === "number" &&
    typeof value.track_key === "string" &&
    Array.isArray(value.cues) &&
    value.cues.every(isStreamCue) &&
    typeof value.done === "number" &&
    typeof value.total === "number"
  );
}

function isTranslationCompletedPayload(
  value: unknown,
): value is PlaybackSubtitleTranslationCompletedPayload {
  return (
    isRecord(value) &&
    typeof value.session_id === "string" &&
    typeof value.file_id === "number" &&
    typeof value.job_id === "number" &&
    typeof value.track_key === "string" &&
    typeof value.subtitle_id === "number" &&
    typeof value.language === "string" &&
    isOptionalString(value.label) &&
    isOptionalSubtitleInventoryItem(value.track)
  );
}

function isTranslationFailedPayload(
  value: unknown,
): value is PlaybackSubtitleTranslationFailedPayload {
  return (
    isRecord(value) &&
    typeof value.session_id === "string" &&
    typeof value.file_id === "number" &&
    typeof value.job_id === "number" &&
    typeof value.track_key === "string" &&
    isOptionalString(value.message)
  );
}

export function parsePlaybackRealtimeMessage(
  data: string,
): PlaybackRealtimeCommandEnvelope | PlaybackRealtimeEventEnvelope | null {
  try {
    const value = JSON.parse(data) as unknown;
    if (!isRecord(value) || typeof value.type !== "string") {
      return null;
    }
    if (value.type === "command") {
      if (
        typeof value.command_id !== "string" ||
        typeof value.session_id !== "string" ||
        !isCommandName(value.name)
      ) {
        return null;
      }
      return {
        type: "command",
        command_id: value.command_id,
        session_id: value.session_id,
        name: value.name,
        reason: typeof value.reason === "string" ? value.reason : undefined,
        issued_by:
          isRecord(value.issued_by) && typeof value.issued_by.kind === "string"
            ? { kind: value.issued_by.kind }
            : undefined,
        deadline_ms: typeof value.deadline_ms === "number" ? value.deadline_ms : undefined,
        payload: isRecord(value.payload) ? value.payload : {},
      };
    }
    if (value.type === "event" && typeof value.session_id === "string") {
      if (
        value.name === "chapter_thumbnail_ready" &&
        isChapterThumbnailReadyPayload(value.payload)
      ) {
        return {
          type: "event",
          session_id: value.session_id,
          name: value.name,
          payload: value.payload,
        };
      }
      if (value.name === "subtitle_ready" && isSubtitleReadyPayload(value.payload)) {
        return {
          type: "event",
          session_id: value.session_id,
          name: value.name,
          payload: value.payload,
        };
      }
      if (value.name === "markers_updated" && isMarkersUpdatedPayload(value.payload)) {
        return {
          type: "event",
          session_id: value.session_id,
          name: value.name,
          payload: value.payload,
        };
      }
      if (
        value.name === "subtitle_translation_started" &&
        isTranslationStartedPayload(value.payload)
      ) {
        return {
          type: "event",
          session_id: value.session_id,
          name: value.name,
          payload: value.payload,
        };
      }
      if (value.name === "subtitle_translation_cues" && isTranslationCuesPayload(value.payload)) {
        return {
          type: "event",
          session_id: value.session_id,
          name: value.name,
          payload: value.payload,
        };
      }
      if (
        value.name === "subtitle_translation_completed" &&
        isTranslationCompletedPayload(value.payload)
      ) {
        return {
          type: "event",
          session_id: value.session_id,
          name: value.name,
          payload: value.payload,
        };
      }
      if (
        value.name === "subtitle_translation_failed" &&
        isTranslationFailedPayload(value.payload)
      ) {
        return {
          type: "event",
          session_id: value.session_id,
          name: value.name,
          payload: value.payload,
        };
      }
    }
    return null;
  } catch {
    return null;
  }
}

export function parsePlaybackRealtimeCommand(data: string): PlaybackRealtimeCommandEnvelope | null {
  const message = parsePlaybackRealtimeMessage(data);
  return message?.type === "command" ? message : null;
}

export function buildPlaybackRealtimeHello(
  sessionId: string,
  commands: PlaybackCommandName[] = SUPPORTED_PLAYBACK_COMMANDS,
): PlaybackRealtimeHelloEnvelope {
  return {
    type: "hello",
    session_id: sessionId,
    client: {
      name: "silo-web",
      version: "1",
    },
    capabilities: {
      commands: [...commands],
    },
  };
}

export function buildPlaybackRealtimeAck(
  sessionId: string,
  commandId: string,
): PlaybackRealtimeAckEnvelope {
  return {
    type: "ack",
    command_id: commandId,
    session_id: sessionId,
    status: "accepted",
  };
}

export function buildPlaybackRealtimeResult(
  sessionId: string,
  commandId: string,
  status: PlaybackRealtimeResultStatus,
  error?: string,
): PlaybackRealtimeResultEnvelope {
  return {
    type: "result",
    command_id: commandId,
    session_id: sessionId,
    status,
    error,
  };
}
