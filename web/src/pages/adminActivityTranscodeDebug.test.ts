import { describe, expect, it } from "vitest";
import type { AdminSession, OperationalLogEntry } from "@/api/types";
import {
  buildTranscodeDebugModel,
  collectTranscodeDebugAttrs,
  formatFFmpegCommand,
} from "./adminActivityTranscodeDebug";

function session(overrides: Partial<AdminSession> = {}): AdminSession {
  return {
    session_id: "session-1",
    user_id: 1,
    username: "admin",
    profile_id: "default",
    media_file_id: 2,
    requested_media_file_id: 2,
    media_title: "Example",
    media_type: "movie",
    play_method: "transcode",
    reporting_node: "local",
    file_duration: 3600,
    started_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    position_seconds: 10,
    is_paused: false,
    audio_track_index: 0,
    transcode_audio: true,
    stream_bitrate_kbps: 6000,
    target_bitrate_kbps: 6000,
    source_bitrate_kbps: 8000,
    source_audio_channels: 6,
    ...overrides,
  };
}

function row(attrs: Record<string, unknown>, id = 1): OperationalLogEntry {
  return {
    id,
    timestamp: new Date().toISOString(),
    level: "info",
    component: "ffmpeg",
    message: "ffmpeg event",
    attrs,
  };
}

describe("admin activity transcode debug", () => {
  it("prefers the newest runtime attributes and keeps session fallbacks", () => {
    const attrs = collectTranscodeDebugAttrs(
      session({ source_video_codec: "h264", target_resolution: "720p" }),
      [
        row({ throttle_paused: true, restart_count: 2 }, 2),
        row({ throttle_paused: false, restart_count: 1, video_encoder: "h264_nvenc" }),
      ],
    );

    expect(attrs).toMatchObject({
      source_video_codec: "h264",
      target_resolution: "720p",
      throttle_paused: true,
      restart_count: 2,
      video_encoder: "h264_nvenc",
    });
  });

  it("formats the adaptive pipeline, pixel path, cache, and throttle policy", () => {
    const model = buildTranscodeDebugModel(session(), [
      row({
        transcode_pipeline: "software_decode_hardware_encode",
        software_video_decode: true,
        video_pixel_formats: ["yuv420p", "nv12"],
        segment_retention_seconds: 120,
        throttle_configured: true,
        throttle_enabled: true,
        throttle_threshold_seconds: 120,
      }),
      row({ throttle_paused: true, throttle_gap_seconds: 124 }, 2),
    ]);

    expect(model.sections).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          title: "Execution",
          facts: expect.arrayContaining([
            expect.objectContaining({ value: "CPU decode → GPU encode" }),
            expect.objectContaining({ label: "CPU decode", value: "Yes" }),
          ]),
        }),
        expect.objectContaining({
          title: "Video",
          facts: expect.arrayContaining([
            expect.objectContaining({ label: "Pixel formats", value: "yuv420p → nv12" }),
          ]),
        }),
        expect.objectContaining({
          title: "HLS & cache",
          facts: expect.arrayContaining([
            expect.objectContaining({ label: "Back cache", value: "120 s" }),
            expect.objectContaining({ label: "Forward buffer", value: "120 s" }),
            expect.objectContaining({ label: "Current lead", value: "124 s" }),
          ]),
        }),
      ]),
    );
  });

  it("renders the exact FFmpeg binary and arguments safely", () => {
    expect(
      formatFFmpegCommand({
        ffmpeg_path: "/usr/bin/ffmpeg",
        ffmpeg_args: ["-i", "/media/My Film.mkv", "-vf", "format=nv12,hwupload_cuda"],
      }),
    ).toBe("/usr/bin/ffmpeg -i '/media/My Film.mkv' -vf format=nv12,hwupload_cuda");

    expect(
      formatFFmpegCommand({
        ffmpeg_path: "/usr/bin/ffmpeg",
        ffmpeg_args: ["-i", "/media/$(id) `uname` it's.mkv"],
      }),
    ).toBe("/usr/bin/ffmpeg -i '/media/$(id) `uname` it'\"'\"'s.mkv'");
  });
});
