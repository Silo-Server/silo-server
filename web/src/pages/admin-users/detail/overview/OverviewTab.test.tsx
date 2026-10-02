// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminSession, AdminUser } from "@/api/types";

import { OverviewTab } from "./OverviewTab";

const mocks = vi.hoisted(() => ({
  capabilities: {} as Record<string, boolean>,
  live: [] as unknown[],
  profiles: [] as unknown[],
  plays: [] as unknown[],
  requestLimit: {} as Record<string, unknown>,
}));

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUserCapabilities: () => ({ data: { available: true, ...mocks.capabilities } }),
}));
vi.mock("@/hooks/queries/admin/userActivity", () => ({
  useAdminUserLiveSessions: () => ({ data: mocks.live, isError: false }),
  useAdminUserProfileActivity: () => ({ data: mocks.profiles, isError: false }),
  useAdminUserRequestUsage: (_id: number, enabled: boolean) => ({
    data: enabled
      ? {
          requests_enabled: true,
          allowed: true,
          unlimited: false,
          used: 12,
          max_requests: 50,
          window_days: 7,
          window_start: "2026-09-22T00:00:00Z",
          remaining: 38,
          auto_approve: true,
        }
      : undefined,
    isError: false,
  }),
  useAdminUserWatchSummary: (_id: number, opts: { enabled?: boolean }) => ({
    data: opts.enabled
      ? {
          days: 30,
          since: "",
          plays: 41,
          completed_plays: 29,
          watched_seconds: 66000,
          last_played_at: null,
        }
      : undefined,
    isError: false,
  }),
  useAdminUserWatchHistory: () => ({
    data: mocks.plays,
    isLoading: false,
    isError: false,
    hasNextPage: false,
    isFetchingNextPage: false,
    fetchNextPage: vi.fn(),
    refetch: vi.fn(),
  }),
  useAdminUserDownloadSummary: (_id: number, enabled: boolean) => ({
    data: enabled
      ? {
          total: 17,
          completed: 9,
          in_progress: 3,
          failed: 0,
          revoked: 0,
          total_bytes: 0,
          devices: 2,
          monitored_series: 3,
        }
      : undefined,
  }),
}));
vi.mock("@/hooks/queries/admin/accessGroups", () => ({ useAccessGroups: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/requests", () => ({
  useRequestSettings: () => ({
    data: {
      requests_enabled: true,
      global_max_requests: 50,
      global_window_days: 7,
      global_auto_approval_enabled: true,
    },
    isError: false,
  }),
  useRequestUserLimit: () => ({ data: mocks.requestLimit, isError: false }),
  useRequestGroupLimit: () => ({ data: undefined, isError: false }),
}));

const USER: AdminUser = {
  id: 42,
  username: "e2e-diagnostics",
  email: "e2e@example.test",
  role: "admin",
  permissions: [],
  enabled: true,
  library_ids: null,
  access_group_id: null,
  max_playback_quality: null,
  max_streams: 2,
  max_transcodes: 1,
  max_remote_stream_bitrate_kbps: null,
  max_local_stream_bitrate_kbps: null,
  transcode_allowed: true,
  audio_transcode_allowed: null,
  max_profiles: 5,
  download_allowed: null,
  download_transcode_allowed: null,
  requests_allowed: null,
  password_login: true,
  password_change_required: false,
  is_owner: false,
  break_glass: false,
  effective_policy: {
    library_ids: null,
    max_playback_quality: "",
    max_streams: 2,
    max_transcodes: 1,
    max_remote_stream_bitrate_kbps: 0,
    max_local_stream_bitrate_kbps: 0,
    transcode_allowed: true,
    audio_transcode_allowed: true,
    download_allowed: true,
    download_transcode_allowed: false,
    requests_allowed: true,
    permissions: ["marker_edit"],
  },
  created_at: "2026-07-20T12:00:00Z",
  updated_at: "2026-09-28T12:00:00Z",
};

const SESSION = {
  session_id: "s1",
  user_id: 42,
  username: "e2e-diagnostics",
  profile_id: "p1",
  profile_name: "Main",
  media_title: "Hello, Dolly",
  media_type: "episode",
  series_name: "Severance",
  season_number: 2,
  episode_number: 4,
  play_method: "direct",
  effective_play_method: "direct",
  file_duration: 3300,
  position_seconds: 1260,
  client_label: "Apple TV 4K",
  client_ip: "192.168.1.40",
} as unknown as AdminSession;

function mount(user: AdminUser = USER) {
  render(
    <MemoryRouter>
      <OverviewTab user={user} />
    </MemoryRouter>,
  );
}
function tile(label: string | RegExp) {
  return screen.getByText(label, { selector: "div.truncate" }).parentElement!;
}

beforeEach(() => {
  mocks.capabilities = { request_usage: true, watch_summary: true, account_downloads: true };
  mocks.requestLimit = { user_id: 7, limit_mode: "inherit", approval_mode: "inherit" };
  mocks.live = [SESSION];
  mocks.profiles = [
    { id: "p1", name: "Main", last_seen_at: "2026-09-29T10:00:00Z" },
    { id: "p2", name: "Kids", last_seen_at: "2026-09-26T10:00:00Z" },
    { id: "p3", name: "Guest", last_seen_at: null },
  ];
  mocks.plays = [
    {
      session_id: "h1",
      profile_id: "p1",
      profile_name: "Main",
      media_item_id: "m1",
      media_title: "Dune: Part Two",
      media_type: "movie",
      series_title: "",
      season_number: null,
      episode_number: null,
      play_method: "transcode",
      started_at: "2026-09-28T19:00:00Z",
      ended_at: "2026-09-28T21:14:00Z",
      watched_seconds: 9000,
      duration_seconds: 9960,
      completed: true,
    },
    {
      session_id: "h2",
      profile_id: "p2",
      profile_name: "Kids",
      media_item_id: "m2",
      media_title: "Tomorrow",
      media_type: "episode",
      series_title: "The Bear",
      season_number: 3,
      episode_number: 2,
      play_method: "direct",
      started_at: "2026-09-26T17:00:00Z",
      ended_at: "2026-09-26T18:02:00Z",
      watched_seconds: 1152,
      duration_seconds: 1800,
      completed: false,
    },
  ];
});
afterEach(() => {
  cleanup();
});

describe("stats", () => {
  it("shows use against limits, with totals only where limited", () => {
    mount();
    expect(tile("Streams now")).toHaveTextContent("Streams now1of 2");
    expect(tile("Transcodes now")).toHaveTextContent("Transcodes now0of 1");
    expect(tile("Profiles")).toHaveTextContent("Profiles3of 5");
    expect(tile("Requests, last 7 days")).toHaveTextContent("12of 50");
    expect(tile("Watched, last 30 days")).toHaveTextContent("18h 20m41 plays");
    cleanup();

    mount({ ...USER, effective_policy: { ...USER.effective_policy, max_streams: 0 } });
    expect(tile("Streams now")).toHaveTextContent(/^Streams now1$/);
  });

  it("reads Off for transcodes when the account may not transcode", () => {
    mount({ ...USER, effective_policy: { ...USER.effective_policy, transcode_allowed: false } });
    expect(tile("Transcodes now")).toHaveTextContent(/^Transcodes nowOff$/);
  });

  it("leaves out tiles this server can't report", () => {
    mocks.capabilities = {};
    mount();
    expect(screen.queryByText(/^Requests/)).toBeNull();
    expect(screen.queryByText("Watched, last 30 days")).toBeNull();
    expect(screen.getByText("Streams now")).toBeInTheDocument();
  });
});

describe("cards", () => {
  it("shows what is playing now and marks that profile", () => {
    mount();
    const now = screen.getByRole("region", { name: "Watching now" });
    expect(now).toHaveTextContent("Severance · S02E04");
    expect(now).toHaveTextContent("Profile Main · Apple TV 4K · 192.168.1.40");
    expect(now).toHaveTextContent("21 of 55 min");
    expect(now).toHaveTextContent("Direct Play");

    const profiles = screen.getByRole("region", { name: "Profiles" });
    expect(profiles).toHaveTextContent("3 of 5 allowed");
    const rows = within(profiles).getAllByText(/Main|Kids|Guest/);
    expect(rows.map((node) => node.closest("div.flex")?.textContent)).toEqual([
      expect.stringMatching(/^MMainWatching now$/),
      expect.stringMatching(/^KKids.+ago$|^KKids\d/),
      "GGuestNo device activity",
    ]);
  });

  it("hides Watching now while nothing plays", () => {
    mocks.live = [];
    mount();
    expect(screen.queryByRole("region", { name: "Watching now" })).toBeNull();
    const profiles = screen.getByRole("region", { name: "Profiles" });
    expect(profiles).not.toHaveTextContent("Watching now");
  });

  it("lists recent plays with the series, method and progress", () => {
    mount();
    const recent = screen.getByRole("region", { name: "Recently watched" });
    expect(recent).toHaveTextContent("Dune: Part Two");
    expect(recent).toHaveTextContent("Main · Transcode · Finished");
    expect(recent).toHaveTextContent("The Bear · S03E02");
    expect(recent).toHaveTextContent("Kids · Direct Play · 64%");
    expect(within(recent).getByRole("link", { name: "All activity →" })).toHaveAttribute(
      "href",
      "/?tab=activity",
    );
  });

  it("says when nothing was watched", () => {
    mocks.plays = [];
    mount();
    expect(screen.getByText("Nothing watched in the last 30 days.")).toBeInTheDocument();
  });

  it("counts and marks a request limit set on the account", () => {
    mocks.requestLimit = {
      user_id: 7,
      limit_mode: "custom",
      max_requests: 5,
      window_days: 7,
      approval_mode: "inherit",
    };
    mount();
    const access = screen.getByRole("region", { name: "Access" });
    expect(access).toHaveTextContent("Server default, with 3 limits set for this account");
    expect(access).toHaveTextContent("5 requests per 7 daysCUSTOM");
    expect(access).toHaveTextContent("Approved automatically");
    expect(access).not.toHaveTextContent("Approved automaticallyCUSTOM");
  });

  it("summarizes access and marks the account's own limits", () => {
    mount();
    const access = screen.getByRole("region", { name: "Access" });
    expect(access).toHaveTextContent("Server default, with 2 limits set for this account");
    expect(access).toHaveTextContent("Can edit markers · can't curate metadata");
    expect(access).toHaveTextContent("Any quality · 2 streamsCUSTOM");
    expect(access).toHaveTextContent("Up to 1 video transcodeCUSTOM · no bitrate cap");
    expect(access).toHaveTextContent(
      "Original files only · 9 downloads confirmed on devices · 3 series monitored",
    );
    expect(access).toHaveTextContent("50 requests per 7 days");
    expect(access).toHaveTextContent("Approved automatically");
  });

  it("leaves the downloads totals out without account downloads", () => {
    mocks.capabilities = { request_usage: true, watch_summary: true };
    mount();
    const access = screen.getByRole("region", { name: "Access" });
    expect(access).toHaveTextContent("Original files only");
    expect(access).not.toHaveTextContent("downloads on");
  });

  it("shows the account's details without who changed it", () => {
    mount();
    const details = screen.getByRole("region", { name: "Details" });
    expect(details).toHaveTextContent("User ID42");
    expect(details).toHaveTextContent(/Last changed(Sep 28, 2026|28 Sep 2026)$/);
  });
});
