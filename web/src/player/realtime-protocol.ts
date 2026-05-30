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
  | "set_subtitle_track";

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
];

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

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function isCommandName(value: unknown): value is PlaybackCommandName {
  return typeof value === "string" && ALL_PLAYBACK_COMMANDS.includes(value as PlaybackCommandName);
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

function isSubtitleReadyPayload(value: unknown): value is PlaybackSubtitleReadyPayload {
  return (
    isRecord(value) &&
    typeof value.session_id === "string" &&
    typeof value.file_id === "number" &&
    typeof value.subtitle_id === "number" &&
    typeof value.language === "string"
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
    typeof value.total_cues === "number"
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
    typeof value.language === "string"
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
    typeof value.track_key === "string"
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

export function buildPlaybackRealtimeHello(sessionId: string): PlaybackRealtimeHelloEnvelope {
  return {
    type: "hello",
    session_id: sessionId,
    client: {
      name: "silo-web",
      version: "1",
    },
    capabilities: {
      commands: [...SUPPORTED_PLAYBACK_COMMANDS],
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
