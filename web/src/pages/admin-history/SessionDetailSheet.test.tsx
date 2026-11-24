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
  it("handles arrow navigation outside the scroll body without hijacking inner focus", () => {
    const onNext = vi.fn();
    const onPrevious = vi.fn();
    render(
      <MemoryRouter>
        <SessionDetailSheet
          open
          onOpenChange={() => {}}
          session={{
            ...baseSession,
            end_reason: "terminated",
            subtitle: {
              ...baseSession.subtitle!,
              delivery: "hls_rendition",
            },
          }}
          hasPrevious
          hasNext
          onPrevious={onPrevious}
          onNext={onNext}
        />
      </MemoryRouter>,
    );

    // Non-burn-in subtitle delivery and end reason render localized labels rather than raw enums.
    expect(screen.getByText("Streamed (HLS)")).toBeTruthy();
    expect(screen.getByText("Stopped by admin")).toBeTruthy();

    fireEvent.keyDown(window, { key: "ArrowDown" });
    expect(onNext).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(window, { key: "ArrowUp" });
    expect(onPrevious).toHaveBeenCalledTimes(1);

    // Arrow keys dispatched from a focused element inside the scroll body must not step sessions.
    const accountLink = screen.getByRole("link", { name: "alice" });
    fireEvent.keyDown(accountLink, { key: "ArrowDown" });
    fireEvent.keyDown(accountLink, { key: "ArrowUp" });
    expect(onNext).toHaveBeenCalledTimes(1);
    expect(onPrevious).toHaveBeenCalledTimes(1);
  });

  it("distinguishes retryable load errors from 404 not-found states", () => {
    const onRetry = vi.fn();
    const { rerender } = render(
      <MemoryRouter>
        <SessionDetailSheet
          open
          onOpenChange={() => {}}
          session={null}
          loadError={new Error("upstream timeout")}
          onRetry={onRetry}
          hasPrevious={false}
          hasNext={false}
          onPrevious={() => {}}
          onNext={() => {}}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("Failed to load session details.")).toBeTruthy();
    expect(screen.getByText("upstream timeout")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onRetry).toHaveBeenCalledTimes(1);

    rerender(
      <MemoryRouter>
        <SessionDetailSheet
          open
          onOpenChange={() => {}}
          session={null}
          notFound
          hasPrevious={false}
          hasNext={false}
          onPrevious={() => {}}
          onNext={() => {}}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText("This playback session could not be found.")).toBeTruthy();
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

    expect(screen.getByText(/Recorded before detailed playback info was tracked/i)).toBeTruthy();
    expect(screen.queryByText(/Local server/i)).toBeNull();
  });

  it("labels transcode bitrate caps honestly and hides unknown caps and unrecorded routes", () => {
    const { rerender } = render(
      <MemoryRouter>
        <SessionDetailSheet
          open
          onOpenChange={() => {}}
          session={baseSession}
          hasPrevious={false}
          hasNext={false}
          onPrevious={() => {}}
          onNext={() => {}}
        />
      </MemoryRouter>,
    );

    expect(screen.getByText(/limit\s+.*8\.0\s+Mbps/)).toBeTruthy();
    // Recorded routes render Silo's native PlaybackRouteBadges without a spurious Unknown network badge.
    expect(screen.getByText("edge-eu-1")).toBeTruthy();
    expect(screen.queryByText(/Unknown/)).toBeNull();

    rerender(
      <MemoryRouter>
        <SessionDetailSheet
          open
          onOpenChange={() => {}}
          session={{
            ...baseSession,
            target_bitrate_kbps: null,
            route_facts_recorded: false,
            reporting_node: null,
            routing_execution: null,
            routing_egress: null,
            routing_egress_node_id: null,
            routing_egress_node_name: null,
          }}
          hasPrevious={false}
          hasNext={false}
          onPrevious={() => {}}
          onNext={() => {}}
        />
      </MemoryRouter>,
    );

    expect(screen.queryByText(/limit\s+.*8\.0\s+Mbps/)).toBeNull();
    // Source bitrate is 42.0 Mbps; delivered must not fall back to it when cap is missing.
    expect(screen.getAllByText(/42\.0\s+Mbps/)).toHaveLength(1);
    expect(screen.queryByText(/Local server/i)).toBeNull();
  });
});
