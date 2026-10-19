import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AdminSession } from "@/api/types";
import { Button } from "@/components/ui/button";
import { AdminSessionActions } from "./AdminSessionActions";

const session = {
  session_id: "example-session",
  user_id: 1,
  username: "Example viewer",
  profile_id: "example-profile",
  media_file_id: 1,
  requested_media_file_id: 1,
  media_title: "Example movie",
  media_type: "movie",
  play_method: "direct_play",
  reporting_node: "local",
  file_duration: 3600,
  started_at: "2026-01-01T12:00:00Z",
  updated_at: "2026-01-01T12:05:00Z",
  position_seconds: 300,
  is_paused: false,
  has_playback_control: true,
  audio_track_index: 0,
  transcode_audio: false,
  stream_bitrate_kbps: null,
  target_bitrate_kbps: null,
  source_bitrate_kbps: null,
  source_audio_channels: 2,
} as AdminSession;

describe("inline session actions", () => {
  it("renders prefix, terminate, and menu buttons together", () => {
    render(
      <AdminSessionActions
        session={session}
        compact
        layout="inline"
        showInlineTerminate
        inlinePrefixActions={<Button size="sm">FFmpeg</Button>}
      />,
    );

    expect(screen.getByRole("button", { name: "FFmpeg" })).toBeVisible();
    expect(screen.getByRole("button", { name: /terminate/i })).toBeVisible();
    expect(screen.getByRole("button", { name: "Session actions" })).toBeVisible();
  });
});
