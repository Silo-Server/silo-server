import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminSession } from "@/api/types";
import { activityMethodMeta } from "./adminActivityPresentation";

const mocks = vi.hoisted(() => ({ sessions: [] as AdminSession[], refresh: vi.fn() }));

vi.mock("@/hooks/queries/admin/stats", () => ({
  useAdminSessions: () => ({ data: mocks.sessions, isLoading: false, refetch: mocks.refresh }),
  useAdminStats: () => ({ data: undefined, isLoading: false }),
}));
vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ connectionState: "live" }),
}));
vi.mock("@/hooks/usePageActivity", () => ({
  usePageActivity: () => ({ canApplyRealtimeUpdates: true }),
}));
vi.mock("@/hooks/queries/admin/ips", () => ({
  useIPUsers: () => ({ data: [], isLoading: false }),
}));
vi.mock("@/hooks/queries/admin/logs", () => ({
  useOperationalLogs: () => ({ data: { entries: [] }, isLoading: false, isFetching: false }),
}));
vi.mock("@/components/AdminSessionActions", () => ({ AdminSessionActions: () => null }));

import AdminActivity from "./AdminActivity";
import AdminStats from "./AdminStats";

function makeSession(overrides: Partial<AdminSession> = {}): AdminSession {
  return {
    session_id: "example-session",
    user_id: 1,
    username: "Example viewer",
    profile_id: "example-profile",
    media_file_id: 1,
    requested_media_file_id: 1,
    media_title: "Example movie",
    media_type: "movie",
    play_method: "remux",
    effective_play_method: "direct_stream",
    reporting_node: "local",
    file_duration: 3600,
    started_at: "2026-01-01T12:00:00Z",
    updated_at: "2026-01-01T12:05:00Z",
    position_seconds: 300,
    is_paused: false,
    audio_track_index: 0,
    transcode_audio: true,
    stream_bitrate_kbps: null,
    target_bitrate_kbps: null,
    source_container: "mkv",
    output_container: "fmp4",
    output_protocol: "hls",
    source_video_codec: "hevc",
    source_video_resolution: "2160p",
    source_audio_codec: "truehd",
    source_audio_channels: 8,
    source_bitrate_kbps: null,
    video_decision: "remux",
    audio_decision: "transcode",
    target_audio_codec: "aac",
    target_audio_channels: 6,
    ...overrides,
  };
}

function renderActivity() {
  return render(
    <MemoryRouter>
      <AdminActivity />
    </MemoryRouter>,
  );
}

describe("activity playback scopes", () => {
  beforeEach(() => {
    mocks.sessions = [makeSession()];
  });
  afterEach(cleanup);

  it.skipIf(!process.env.SILO_ACTIVITY_EVIDENCE_DIR)(
    "exports synthetic activity screenshot fixtures",
    async () => {
      const directory = process.env.SILO_ACTIVITY_EVIDENCE_DIR!;
      const baseline = process.env.SILO_ACTIVITY_EVIDENCE_BASELINE === "1";
      const methods = ["direct_stream", "direct", "remux", "transcode"];
      mocks.sessions = methods.map((method, index) =>
        makeSession({
          session_id: `example-session-${index}`,
          username: `Example viewer ${index + 1}`,
          media_title: `Example video ${index + 1}`,
          effective_play_method: baseline && method === "direct_stream" ? "audio" : method,
          play_method: method === "direct_stream" ? "remux" : method,
          video_decision: method === "direct" || method === "transcode" ? method : "remux",
          audio_decision:
            method === "direct_stream" || method === "transcode" ? "transcode" : method,
          transcode_audio: method === "direct_stream" || method === "transcode",
          output_container: baseline
            ? undefined
            : method === "direct"
              ? "mkv"
              : method === "transcode"
                ? "mpegts"
                : "fmp4",
          output_protocol: baseline ? undefined : method === "direct" ? "http" : "hls",
          target_video_codec: method === "transcode" ? "h264" : undefined,
          started_at: new Date(Date.now() - 300_000).toISOString(),
          updated_at: new Date().toISOString(),
        }),
      );
      renderActivity();
      await mkdir(directory, { recursive: true });
      await writeFile(join(directory, "collapsed.html"), document.body.innerHTML);
      fireEvent.click(screen.getAllByRole("button", { name: "Details" })[0]!);
      await writeFile(join(directory, "expanded.html"), document.body.innerHTML);
    },
  );

  it("shows Direct Stream on desktop and mobile without a second audio-transcode badge", () => {
    renderActivity();

    const badges = screen.getAllByLabelText("Playback method: Direct Stream");
    expect(badges).toHaveLength(2);
    for (const badge of badges) {
      expect(badge).toHaveTextContent("Direct Stream");
      expect(badge).toHaveAttribute("title", activityMethodMeta("direct_stream").description);
      expect(badge.className).toContain(activityMethodMeta("direct_stream").badgeClass);
      expect(within(badge.parentElement!).getByRole("button", { name: "Details" })).toBeTruthy();
    }
    expect(screen.queryByText("Audio Transcode")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Playback method: Direct Play")).not.toBeInTheDocument();
    expect(screen.getAllByText("Copy", { exact: true })).toHaveLength(2);
    expect(screen.getAllByText("Transcode", { exact: true })).toHaveLength(2);
    expect(screen.getAllByText("AAC 5.1", { exact: true })).toHaveLength(2);

    fireEvent.click(screen.getAllByRole("button", { name: "Details" })[0]!);
    expect(screen.getByText("Audio Transcode")).toBeInTheDocument();
    expect(screen.getByText("Copied without re-encoding")).toBeInTheDocument();
    expect(screen.getByText("MKV → fMP4 (HLS)")).toBeInTheDocument();
    expect(screen.queryByText("Unknown output container")).not.toBeInTheDocument();
    expect(screen.queryByText("MKV → Remux")).not.toBeInTheDocument();
  });

  it("uses the same four labels, counts and colors for the bar, filters and row badges", () => {
    const methods = ["direct", "remux", "direct_stream", "transcode"];
    mocks.sessions = methods.map((method) =>
      makeSession({
        session_id: method,
        effective_play_method: method,
        play_method: method === "direct_stream" ? "remux" : method,
        video_decision: method === "direct" || method === "transcode" ? method : "remux",
        audio_decision: method === "direct_stream" || method === "transcode" ? "transcode" : method,
        transcode_audio: method === "direct_stream" || method === "transcode",
      }),
    );
    renderActivity();

    const bar = screen.getByRole("img", { name: "Playback method distribution" });
    expect(Array.from(bar.children, (segment) => segment.getAttribute("title"))).toEqual(
      methods.map((method) => `${activityMethodMeta(method).label}: 1`),
    );
    for (const method of methods) {
      const meta = activityMethodMeta(method);
      const segment = within(bar).getByTitle(`${meta.label}: 1`);
      expect(segment).toHaveStyle({ width: "25%" });
      expect(segment).toHaveClass(meta.swatchClass);
      expect(screen.getAllByLabelText(`Playback method: ${meta.label}`)).toHaveLength(2);
      expect(screen.getByRole("button", { name: `${meta.label} 1` })).toHaveAttribute(
        "aria-pressed",
        "false",
      );
    }
    fireEvent.click(screen.getByRole("button", { name: "Direct Stream 1" }));
    expect(screen.getByRole("button", { name: "Direct Stream 1" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByText("Showing 1 of 4 streams")).toBeInTheDocument();
    expect(screen.getAllByLabelText("Playback method: Direct Stream")).toHaveLength(2);
    expect(screen.queryByLabelText("Playback method: Direct Play")).not.toBeInTheDocument();
    expect(bar.children).toHaveLength(4);
  });

  it("keeps unknown scopes unknown and puts encoder and tone-map modes in expanded details", () => {
    mocks.sessions = [
      makeSession({ session_id: "unknown", effective_play_method: "future-method" }),
      makeSession({
        session_id: "video-transcode",
        effective_play_method: "transcode",
        video_decision: "transcode",
        transcode_hw_accel: "qsv",
        tone_map_mode: "hardware",
      }),
    ];
    renderActivity();

    expect(screen.getAllByLabelText("Playback method: Unknown")).toHaveLength(2);
    expect(screen.getAllByLabelText("Playback method: Transcode")).toHaveLength(2);
    expect(screen.queryByText("HW QSV")).not.toBeInTheDocument();
    expect(screen.queryByText("HW Tone map")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Playback method: Direct Play")).not.toBeInTheDocument();
    const badge = screen.getAllByLabelText("Playback method: Transcode")[0]!;
    fireEvent.click(within(badge.parentElement!).getByRole("button", { name: "Details" }));
    expect(screen.getByText("HW QSV")).toBeInTheDocument();
    expect(screen.getByText("Hardware")).toBeInTheDocument();
  });

  it("does not collapse distinct server sessions just because their display fields match", () => {
    mocks.sessions = [makeSession(), makeSession({ session_id: "another-session" })];
    renderActivity();

    expect(screen.getByRole("button", { name: "Direct Stream 2" })).toBeInTheDocument();
    expect(screen.getAllByLabelText("Playback method: Direct Stream")).toHaveLength(4);
  });

  it("uses the display label rather than the raw method key in System stats", () => {
    render(
      <MemoryRouter>
        <AdminStats />
      </MemoryRouter>,
    );

    expect(screen.getByRole("cell", { name: "Direct Stream" })).toBeInTheDocument();
    expect(screen.queryByText("direct_stream")).not.toBeInTheDocument();
  });
});
