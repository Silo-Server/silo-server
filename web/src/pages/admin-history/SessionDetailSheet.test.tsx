import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";

import type { AdminPlaybackHistoryItem } from "@/api/types";
import { SessionDetailSheet } from "./SessionDetailSheet";

afterEach(() => {
  cleanup();
});

const baseSession: AdminPlaybackHistoryItem = {
  session_id: "sess-12345678-abcd",
  user_id: 7,
  username: "alice",
  profile_id: "p-alice",
  profile_name: "Alice",
  media_item_id: "movie-1",
  media_file_id: 42,
  media_title: "Dune",
  media_type: "movie",
  play_method: "transcode",
  started_at: "2026-11-20T20:00:00Z",
  ended_at: "2026-11-20T21:00:00Z",
  end_position_seconds: 3400,
  duration_seconds: 9000,
  completed: false,
  played_seconds: 3200,
  measured: true,
  paused_seconds: 120,
  client_ip: "203.0.113.42",
  client_name: "Chrome",
  client_version: "131",
  device_platform: "web",
  video_decision: "transcode",
  audio_decision: "transcode",
  end_reason: "stopped",
  series_title: "",
  season_number: null,
  episode_number: null,
  subtitle: {
    track_id: "file:42:subtitle:0",
    language: "en",
    codec: "hdmv_pgs_subtitle",
    format: null,
    source: "embedded",
    delivery: "burn_in",
    forced: false,
    hearing_impaired: false,
  },
  target_video_codec: "h264",
  target_resolution: "1080p",
  target_audio_codec: "aac",
  target_audio_channels: 2,
  output_container: "fmp4",
  output_protocol: "hls",
  target_bitrate_kbps: 8000,
  transcode_hw_accel: "vaapi",
  tone_map_mode: "hardware",
  adaptation_reasons: ["video_codec_unsupported", "subtitle_burn_in"],
  reporting_node: "api-1",
  stream_location: "remote",
  compat_origin: true,
  is_jellyfin_client: true,
  routing_execution: "local",
  routing_egress: "proxy",
  routing_egress_node_id: 3,
  routing_egress_node_name: "edge-eu-1",
  route_facts_recorded: true,
  source_video_codec: "hevc",
  source_resolution: "2160p",
  source_audio_codec: "truehd",
  source_audio_channels: 8,
  source_container: "mkv",
  source_bitrate_kbps: 42000,
  source_hdr: true,
  source_video_range_type: "HDR10",
  library_id: 1,
  library_name: "Movies",
};

describe("SessionDetailSheet", () => {
  it("renders source vs delivered specs, adaptation reasons, route, and handles arrow navigation", () => {
    const onNext = vi.fn();
    const onPrevious = vi.fn();
    render(
      <MemoryRouter>
        <SessionDetailSheet
          open
          onOpenChange={() => {}}
          session={baseSession}
          hasPrevious
          hasNext
          onPrevious={onPrevious}
          onNext={onNext}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("Dune")).toBeTruthy();
    expect(screen.getByText("HDR10")).toBeTruthy();
    expect(screen.getByText("Burned in")).toBeTruthy();
    expect(screen.getByText("JF")).toBeTruthy();
    expect(screen.getByText("Video codec not supported")).toBeTruthy();
    expect(screen.getByText("Subtitles burned in")).toBeTruthy();
    expect(screen.getByText("Movies")).toBeTruthy();
    expect(screen.getByText("203.0.113.42")).toBeTruthy();

    fireEvent.keyDown(window, { key: "ArrowDown" });
    expect(onNext).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(window, { key: "ArrowUp" });
    expect(onPrevious).toHaveBeenCalledTimes(1);
  });

  it("shows the pre-upgrade notice for legacy rows without route facts", () => {
    render(
      <MemoryRouter>
        <SessionDetailSheet
          open
          onOpenChange={() => {}}
          session={{
            ...baseSession,
            route_facts_recorded: false,
            adaptation_reasons: [],
            subtitle: null,
            is_jellyfin_client: false,
            transcode_hw_accel: null,
            tone_map_mode: null,
            source_video_range_type: null,
          }}
          hasPrevious={false}
          hasNext={false}
          onPrevious={() => {}}
          onNext={() => {}}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText(/Recorded before detailed route telemetry was captured/i)).toBeTruthy();
  });
});
