import { describe, expect, it } from "vitest";
import type { AdminSession } from "@/api/types";
import { getPrimaryPlaybackAction } from "./adminSessionActionModel";

const baseSession: AdminSession = {
  session_id: "session-1",
  user_id: 1,
  username: "alex",
  profile_id: "profile-1",
  media_file_id: 100,
  requested_media_file_id: 100,
  media_title: "Heat",
  media_type: "movie",
  play_method: "direct",
  reporting_node: "node-1",
  file_duration: 3600,
  started_at: "2026-03-24T12:00:00Z",
  updated_at: "2026-03-24T12:05:00Z",
  position_seconds: 300,
  is_paused: false,
  has_playback_control: true,
  audio_track_index: 0,
  transcode_audio: false,
  stream_bitrate_kbps: null,
  target_bitrate_kbps: null,
  source_audio_channels: null,
  source_bitrate_kbps: null,
};

describe("getPrimaryPlaybackAction", () => {
  it("returns resume for paused sessions", () => {
    expect(
      getPrimaryPlaybackAction({
        ...baseSession,
        is_paused: true,
      }),
    ).toEqual({ action: "resume", label: "Resume" });
  });

  it("returns pause for active sessions", () => {
    expect(getPrimaryPlaybackAction(baseSession)).toEqual({ action: "pause", label: "Pause" });
  });
});
