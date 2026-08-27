import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { AdminLiveSessionsResponse, AdminSession } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useAdminLiveSessions: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/stats", () => ({
  useAdminLiveSessions: (includeHidden: boolean) => mocks.useAdminLiveSessions(includeHidden),
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
  useOperationalLogs: () => ({ data: { entries: [] }, isLoading: false }),
}));

import AdminActivity from "./AdminActivity";

const session = {
  session_id: "session-1",
  user_id: 7,
  username: "viewer",
  profile_id: "profile-1",
  media_file_id: 42,
  requested_media_file_id: 42,
  media_title: "Live Endpoint Movie",
  media_type: "movie",
  play_method: "directplay",
  reporting_node: "node-1",
  file_duration: 7200,
  started_at: "2026-08-27T10:00:00Z",
  updated_at: "2026-08-27T10:01:00Z",
  position_seconds: 60,
  is_paused: false,
  audio_track_index: 0,
  transcode_audio: false,
  stream_bitrate_kbps: 8000,
  target_bitrate_kbps: null,
  source_bitrate_kbps: 8000,
  source_audio_channels: 2,
} as AdminSession;

const envelope: AdminLiveSessionsResponse = {
  telemetry_enabled: true,
  view_available: true,
  view_complete: true,
  view_stale: false,
  view_age_ms: 0,
  no_delivery_count: 0,
  no_delivery_shown: true,
  sessions: [session],
};

describe("AdminActivity", () => {
  it("renders sessions from the live endpoint including idle rows", () => {
    mocks.useAdminLiveSessions.mockReturnValue({
      data: envelope,
      isLoading: false,
      refetch: vi.fn(),
    });

    render(
      <MemoryRouter>
        <AdminActivity />
      </MemoryRouter>,
    );

    expect(mocks.useAdminLiveSessions).toHaveBeenCalledWith(true);
    expect(screen.getAllByText("Live Endpoint Movie")).not.toHaveLength(0);
    expect(screen.getByText("measured")).toBeInTheDocument();
  });
});
