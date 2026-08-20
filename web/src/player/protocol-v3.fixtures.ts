/**
 * Minimal, valid v3 objects for tests.
 *
 * These are deliberately hand-written rather than copied from the Go golden
 * fixtures: their job is to be the smallest thing that satisfies the types, so
 * a test can override the one field it is actually about. The cross-platform
 * conformance corpus lives in `internal/playback/testdata/protocol_v3/` and is
 * a separate concern.
 */

import type {
  ClientCodecCapabilitiesV3,
  ClientPlaybackContextV3,
  PlanV3,
  SubtitleInventoryItemV3,
} from "./protocol-v3";
import { PROTOCOL_V3 } from "./protocol-v3";

export function fixtureClientCapabilitiesV3(
  overrides: Partial<ClientCodecCapabilitiesV3> = {},
): ClientCodecCapabilitiesV3 {
  return {
    video_evidence: "declared",
    audio_evidence: "declared",
    codecs_video: ["h264"],
    codecs_video_hardware: [],
    codecs_audio: ["aac"],
    containers: ["mp4"],
    hdr: false,
    ...overrides,
  };
}

export function fixtureClientPlaybackContextV3(
  overrides: Partial<ClientPlaybackContextV3> = {},
): ClientPlaybackContextV3 {
  return {
    protocol_version: PROTOCOL_V3,
    form_factor: "desktop",
    app_version: "test",
    device: { platform: "web" },
    output: {},
    deliveries: {
      hls: {
        enabled: true,
        supported_on_device: true,
        containers: ["mp4"],
        video_codecs: ["h264"],
        audio_decode_codecs: ["aac"],
        audio_passthrough_codecs: [],
        subtitles: {
          embedded_text: false,
          sidecar_text: true,
          ass_styling: true,
          embedded_bitmap: false,
          sidecar_bitmap: false,
          font_attachments: true,
        },
        features: [],
        auth_header_refresh: false,
        validated_claims: [],
        transformations: [],
      },
    },
    ...overrides,
  };
}

export function fixtureSubtitleInventoryItemV3(
  overrides: Partial<SubtitleInventoryItemV3> = {},
): SubtitleInventoryItemV3 {
  return {
    track_id: "file:7:subtitle:0",
    combined_index: 0,
    source: "embedded",
    codec: "subrip",
    language: "eng",
    label: "English",
    forced: false,
    default: false,
    hearing_impaired: false,
    delivery: "sidecar",
    url: "/stream/session-1/subtitles/0.vtt?file_id=7",
    ...overrides,
  };
}

export function fixturePlanV3(overrides: Partial<PlanV3> = {}): PlanV3 {
  return {
    protocol_version: PROTOCOL_V3,
    plan_id: "plan:0123456789abcdef",
    plan_attempt_key: "v3:0123456789abcdef",
    session_id: "session-1",
    delivery: "server_transcode_hls",
    stream: {
      url: "/stream/session-1/master.m3u8",
      protocol: "hls",
      mime_type: "application/vnd.apple.mpegurl",
      headers: {},
      header_refresh: "none",
    },
    timeline: {
      source_start_seconds: 0,
      stream_origin_seconds: 0,
      player_start_seconds: 0,
      timeline_offset_seconds: 0,
      can_seek_anywhere: true,
      seek_restoration: "player_position",
    },
    selected_tracks: {
      audio: { id: "file:7:audio:0", index: 0 },
    },
    effective_recipe: { video_codec: "h264", audio_codec: "aac", height: 1080 },
    claims: {
      video: { hdr10: false, hdr10_plus: false, hlg: false, dolby_vision: false },
      audio: { codec: "aac", passthrough: false, atmos_preserved: false },
      subtitles: { ass_styling_preserved: false, bitmap_overlay: false, bitmap_sidecar: false },
    },
    subtitle: { mode: "off", inventory: [] },
    transformations: [],
    applied_quirks: [],
    runtime_corrections: [],
    available_qualities: [{ label: "original", preserves_source: true }],
    degradation_warnings: [],
    decision_reason: "test",
    requested_media_file_id: 7,
    effective_media_file_id: 7,
    source: {
      media_file_id: 7,
      duration_seconds: 3600,
      hdr10_plus: false,
      dv_enhancement_layer: "none",
    },
    subtitle_fidelity_policy: "preserve",
    ...overrides,
  };
}
