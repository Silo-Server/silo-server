import { describe, expect, it } from "vitest";
import type { AdminSession } from "@/api/types";
import {
  classifyActivityMethod,
  compareActivityMethods,
  isJellyfinSession,
  formatContainerDetail,
  formatDeliveredAudioSummary,
  formatDeliveredContainerSummary,
  formatDeliveredVideoSummary,
  formatPlaybackDecisionSummary,
  formatSourceContainerSummary,
  formatTranscodeModeSummary,
  formatVideoDetail,
  formatVideoSummary,
  normalizeContainerDecision,
  normalizeStreamDecision,
} from "./adminActivityPresentation";

function makeSession(overrides: Partial<AdminSession> = {}): AdminSession {
  return {
    session_id: overrides.session_id ?? "session-1",
    user_id: overrides.user_id ?? 1,
    username: overrides.username ?? "admin",
    profile_id: overrides.profile_id ?? "default",
    media_file_id: overrides.media_file_id ?? 2,
    requested_media_file_id: overrides.requested_media_file_id ?? 2,
    media_title: overrides.media_title ?? "Example",
    media_type: overrides.media_type ?? "movie",
    play_method: overrides.play_method ?? "transcode",
    reporting_node: overrides.reporting_node ?? "local",
    file_duration: overrides.file_duration ?? 3600,
    started_at: overrides.started_at ?? new Date().toISOString(),
    updated_at: overrides.updated_at ?? new Date().toISOString(),
    position_seconds: overrides.position_seconds ?? 300,
    is_paused: overrides.is_paused ?? false,
    client_name: overrides.client_name,
    client_user_agent: overrides.client_user_agent,
    audio_track_index: overrides.audio_track_index ?? 0,
    transcode_audio: overrides.transcode_audio ?? true,
    stream_bitrate_kbps: overrides.stream_bitrate_kbps ?? 8000,
    target_bitrate_kbps: overrides.target_bitrate_kbps ?? 8000,
    transcode_hw_accel: overrides.transcode_hw_accel,
    source_container: overrides.source_container ?? "mkv",
    source_bitrate_kbps: overrides.source_bitrate_kbps ?? 9000,
    source_video_codec: overrides.source_video_codec ?? "h264",
    source_video_resolution: overrides.source_video_resolution ?? "1080p",
    video_decision: overrides.video_decision,
    target_video_codec: overrides.target_video_codec ?? "h264",
    target_resolution: overrides.target_resolution ?? "1080p",
    source_audio_codec: overrides.source_audio_codec ?? "aac",
    source_audio_channels: overrides.source_audio_channels ?? 2,
    audio_decision: overrides.audio_decision,
    target_audio_codec: overrides.target_audio_codec,
    requested_video_codec: overrides.requested_video_codec ?? "hevc",
    requested_video_resolution: overrides.requested_video_resolution ?? "2160p",
  };
}

describe("adminActivityPresentation", () => {
  it("uses the effective source as the primary video summary", () => {
    expect(formatVideoSummary(makeSession())).toBe("H.264 · 1080p");
  });

  it("explains auto-switched requested sources in the detail line", () => {
    expect(
      formatVideoDetail(
        makeSession({
          media_file_id: 2,
          requested_media_file_id: 1,
        }),
      ),
    ).toBe("Auto-switched from HEVC · 2160p · Output → H.264 · 1080p");
  });

  it("summarizes the delivered transcode output for compact rows", () => {
    const session = makeSession({
      source_video_codec: "hevc",
      source_video_resolution: "2160p",
      target_video_codec: "h264",
      target_resolution: "1080p",
      source_audio_codec: "eac3",
      source_audio_channels: 6,
      target_audio_codec: "aac",
    });

    expect(formatPlaybackDecisionSummary(session)).toBe("transcode");
    expect(normalizeContainerDecision(session.play_method)).toBe("hls");
    expect(normalizeStreamDecision(session.video_decision || session.play_method)).toBe(
      "transcode",
    );
    expect(
      normalizeStreamDecision(
        session.audio_decision || (session.transcode_audio ? "transcode" : session.play_method),
      ),
    ).toBe("transcode");
    expect(formatSourceContainerSummary(session)).toBe("MKV");
    expect(formatDeliveredContainerSummary(session)).toBe("HLS");
    expect(formatContainerDetail(session)).toBe("MKV → HLS");
    expect(formatDeliveredVideoSummary(session)).toBe("H.264 · 1080p");
    expect(formatDeliveredAudioSummary(session)).toBe("AAC 5.1");
    expect(formatTranscodeModeSummary(session)).toBe("HW/SW unknown");
  });

  it("keeps direct playback summaries on the effective source", () => {
    const session = makeSession({
      play_method: "direct",
      video_decision: "direct",
      audio_decision: "direct",
      transcode_audio: false,
      source_video_codec: "hevc",
      source_video_resolution: "2160p",
      target_video_codec: "h264",
      target_resolution: "1080p",
      source_audio_codec: "eac3",
      source_audio_channels: 6,
      target_audio_codec: "aac",
    });

    expect(formatPlaybackDecisionSummary(session)).toBe("direct");
    expect(normalizeContainerDecision(session.play_method)).toBe("direct");
    expect(normalizeStreamDecision(session.video_decision)).toBe("direct");
    expect(normalizeStreamDecision(session.audio_decision)).toBe("direct");
    expect(formatDeliveredContainerSummary(session)).toBe("MKV");
    expect(formatContainerDetail(session)).toBe("Original container");
    expect(formatDeliveredVideoSummary(session)).toBe("HEVC · 2160p");
    expect(formatDeliveredAudioSummary(session)).toBe("EAC3 5.1");
    expect(formatTranscodeModeSummary(session)).toBeNull();
  });

  it("labels hardware and software transcode modes", () => {
    expect(formatTranscodeModeSummary(makeSession({ transcode_hw_accel: "qsv" }))).toBe("HW QSV");
    expect(formatTranscodeModeSummary(makeSession({ transcode_hw_accel: "none" }))).toBe("SW");
    expect(
      formatTranscodeModeSummary(
        makeSession({
          play_method: "remux",
          video_decision: "remux",
          audio_decision: "transcode",
          transcode_audio: true,
          transcode_hw_accel: "qsv",
        }),
      ),
    ).toBe("Audio SW");
  });

  it("buckets activity sessions by the backend's per-stream decisions", () => {
    // Only the audio stream re-encoded (video copied) → "audio".
    expect(
      classifyActivityMethod(
        makeSession({
          play_method: "remux",
          video_decision: "remux",
          audio_decision: "transcode",
          transcode_audio: true,
        }),
      ),
    ).toBe("audio");
    // Same audio-only shape reported via the HLS path (play_method "transcode",
    // video copied) still counts as an audio transcode, not video.
    expect(
      classifyActivityMethod(
        makeSession({
          play_method: "transcode",
          video_decision: "remux",
          audio_decision: "transcode",
          transcode_audio: true,
        }),
      ),
    ).toBe("audio");

    // Full video transcode (with or without audio) stays in the "transcode" bucket.
    expect(
      classifyActivityMethod(
        makeSession({
          play_method: "transcode",
          video_decision: "transcode",
          audio_decision: "transcode",
          transcode_audio: true,
        }),
      ),
    ).toBe("transcode");

    // Video-copy HLS repackage (play_method "transcode" but nothing re-encoded)
    // must count as remux, NOT a video transcode.
    expect(
      classifyActivityMethod(
        makeSession({
          play_method: "transcode",
          video_decision: "remux",
          audio_decision: "remux",
          transcode_audio: false,
        }),
      ),
    ).toBe("remux");

    // Direct play and plain remux keep their expected buckets.
    expect(
      classifyActivityMethod(
        makeSession({
          play_method: "direct",
          video_decision: "direct",
          audio_decision: "direct",
          transcode_audio: false,
        }),
      ),
    ).toBe("direct");
    expect(
      classifyActivityMethod(
        makeSession({
          play_method: "remux",
          video_decision: "remux",
          audio_decision: "remux",
          transcode_audio: false,
        }),
      ),
    ).toBe("remux");
  });

  it("orders activity buckets with audio after video transcode", () => {
    const sorted = ["audio", "transcode", "direct", "remux"].sort(compareActivityMethods);
    expect(sorted).toEqual(["direct", "remux", "transcode", "audio"]);
  });

  it("tags Jellyfin-ecosystem clients for the JF pill", () => {
    // Jellyfin clients report their name via the MediaBrowser auth header.
    expect(isJellyfinSession(makeSession({ client_name: "Jellyfin Web" }))).toBe(true);
    expect(isJellyfinSession(makeSession({ client_name: "Findroid" }))).toBe(true);
    // Name-less Static=true direct play (Infuse) is still identified by user agent.
    expect(
      isJellyfinSession(
        makeSession({ client_name: undefined, client_user_agent: "Infuse-Direct/8.4.6" }),
      ),
    ).toBe(true);
    // Native clients and generic browsers are not Jellyfin.
    expect(isJellyfinSession(makeSession({ client_name: "Silo Android" }))).toBe(false);
    expect(
      isJellyfinSession(
        makeSession({
          client_name: undefined,
          client_user_agent: "Mozilla/5.0 (X11) Chrome/120.0 Safari/537.36",
        }),
      ),
    ).toBe(false);
    // No client metadata at all → not Jellyfin.
    expect(isJellyfinSession(makeSession())).toBe(false);
  });

  it("labels HLS copy-original sessions as container HLS with copied video", () => {
    const session = makeSession({
      play_method: "transcode",
      video_decision: "remux",
      audio_decision: "transcode",
      target_video_codec: "copy",
      target_audio_codec: "aac",
      transcode_audio: true,
    });

    expect(normalizeContainerDecision(session.play_method)).toBe("hls");
    expect(normalizeStreamDecision(session.video_decision)).toBe("copy");
    expect(normalizeStreamDecision(session.audio_decision)).toBe("transcode");
    expect(formatDeliveredContainerSummary(session)).toBe("HLS");
    expect(formatVideoDetail(session)).toBe("Video stream copied");
  });
});
