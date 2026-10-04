import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminSession } from "@/api/types";
import type { AdminDownloadPreparationList } from "@/api/v2/adminDownloadPreparations";
import { makePreparation, makePreparationList } from "@/test/downloadPreparations";
import { activityMethodMeta } from "./adminActivityPresentation";

const mocks = vi.hoisted(() => ({
  sessions: [] as AdminSession[],
  refresh: vi.fn(),
  error: false,
  rows: true,
  next: vi.fn(),
  restart: vi.fn(),
  preparations: undefined as AdminDownloadPreparationList | undefined,
  preparationsRefetch: vi.fn(),
  preparationAction: vi.fn(),
  toastSuccess: vi.fn(),
  logParams: [] as unknown[],
}));

vi.mock("@/hooks/queries/admin/stats", () => ({
  useAdminSessions: () => ({ data: mocks.sessions, isLoading: false, refetch: mocks.refresh }),
  useAdminStats: () => ({ data: undefined, isLoading: false }),
}));
vi.mock("@/hooks/queries/admin/downloadPreparations", () => ({
  useAdminDownloadPreparations: () => ({
    data: mocks.preparations,
    isLoading: false,
    isError: false,
    refetch: mocks.preparationsRefetch,
  }),
  useAdminDownloadPreparationAction: () => ({
    mutate: mocks.preparationAction,
    isPending: false,
    variables: undefined,
  }),
}));
vi.mock("sonner", () => ({
  toast: { success: mocks.toastSuccess, info: vi.fn(), error: vi.fn() },
}));
vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ connectionState: "live" }),
}));
vi.mock("@/hooks/usePageActivity", () => ({
  usePageActivity: () => ({ canApplyRealtimeUpdates: true }),
}));
vi.mock("@/hooks/queries/admin/ips", () => ({
  useIPUsers: () => ({
    data: mocks.rows
      ? [
          {
            user_id: 7,
            username: "Target",
            first_seen: "2026-01-01T00:00:00Z",
            last_seen: "2026-01-02T00:00:00Z",
            request_count: 3,
          },
        ]
      : [],
    isLoading: false,
    isError: mocks.error,
    hasNextPage: true,
    isFetchingNextPage: false,
    fetchNextPage: mocks.next,
    restart: mocks.restart,
  }),
}));
vi.mock("@/hooks/queries/admin/logs", () => ({
  useOperationalLogs: (params: unknown, enabled: boolean) => {
    if (enabled) mocks.logParams.push(params);
    return { data: { entries: [] }, isLoading: false, isFetching: false };
  },
}));
vi.mock("@/components/AdminSessionActions", () => ({ AdminSessionActions: () => null }));

import AdminActivity from "./AdminActivity";

beforeEach(() => {
  vi.clearAllMocks();
  mocks.sessions = [];
  mocks.error = false;
  mocks.rows = true;
  mocks.preparations = undefined;
  mocks.logParams = [];
});
afterEach(cleanup);

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

  it("shows the server's local or remote classification on each stream", () => {
    mocks.sessions = [
      makeSession({ session_id: "local", stream_location: "local" }),
      makeSession({ session_id: "remote", stream_location: "remote" }),
    ];
    renderActivity();

    expect(screen.getAllByLabelText("Stream location: Local")).toHaveLength(2);
    expect(screen.getAllByLabelText("Stream location: Remote")).toHaveLength(2);
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
});

describe("IP lookup", () => {
  function lookup() {
    render(
      <MemoryRouter>
        <AdminActivity />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByText("IP Lookup"));
    fireEvent.change(screen.getByPlaceholderText(/IP lookup/), {
      target: { value: "198.51.100.1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Lookup" }));
  }

  it("offers explicit continuation and preserves existing rows on partial errors", () => {
    mocks.error = true;
    lookup();
    expect(screen.getByText("Target")).toBeInTheDocument();
    expect(screen.getByText(/Could not load more history/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(mocks.next).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Reload history" }));
    expect(mocks.restart).toHaveBeenCalledTimes(1);
  });

  it("does not present a failed initial page as an empty result", () => {
    mocks.error = true;
    mocks.rows = false;
    lookup();
    expect(screen.getByText(/Could not load IP history/)).toBeInTheDocument();
    expect(screen.queryByText(/No users found/)).not.toBeInTheDocument();
  });
});

describe("download preparation tab", () => {
  function renderActivity(path = "/admin/activity") {
    render(
      <MemoryRouter initialEntries={[path]}>
        <AdminActivity />
      </MemoryRouter>,
    );
  }

  it("filters by state and search text", () => {
    mocks.preparations = makePreparationList([
      makePreparation(),
      makePreparation({
        id: "art-q",
        state: "queued",
        progress: undefined,
        media_title: "Queued One",
      }),
    ]);
    renderActivity("/admin/activity?view=preparations");

    fireEvent.click(screen.getByRole("button", { name: /Queued\s*1/ }));
    expect(screen.getByText("Showing 1 of 2 jobs")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();

    fireEvent.click(screen.getByText("Clear filters"));
    fireEvent.change(screen.getByLabelText("Filter download preparation"), {
      target: { value: "alex's iphone" },
    });
    expect(screen.getByText("Showing 2 of 2 jobs")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Filter download preparation"), {
      target: { value: "nothing matches" },
    });
    expect(screen.getByText("No jobs match your filters")).toBeInTheDocument();
  });

  it("expands job details with the FFmpeg console and log link", () => {
    mocks.preparations = makePreparationList([makePreparation()]);
    renderActivity("/admin/activity?view=preparations");

    fireEvent.click(screen.getAllByRole("button", { name: "Details for Example Movie" })[0]!);
    expect(screen.getByText("All 2 tracks → stereo AAC")).toBeInTheDocument();
    expect(screen.getByText("HDR → SDR (software)")).toBeInTheDocument();
    expect(screen.getByText("25:00 of 1:40:00 (25%)")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /View logs/ })).toHaveAttribute(
      "href",
      "/admin/logs?playback_session_id=download-prepare-art-1&component=ffmpeg",
    );

    fireEvent.click(screen.getByRole("button", { name: /FFmpeg/ }));
    expect(mocks.logParams).toContainEqual({
      playback_session_id: "download-prepare-art-1",
      component: "ffmpeg",
      limit: 12,
    });
    expect(screen.getByText(/No FFmpeg output for this job yet/)).toBeInTheDocument();
  });

  it("pauses, resumes and cancels one job from its row", () => {
    mocks.preparationAction.mockImplementation(
      (
        { ids }: { ids: string[] },
        options: { onSuccess: (results: { id: string; outcome: string }[]) => void },
      ) => options.onSuccess(ids.map((id) => ({ id, outcome: "applied" }))),
    );
    mocks.preparations = makePreparationList([
      makePreparation(),
      makePreparation({
        id: "art-p",
        state: "paused",
        progress: undefined,
        media_title: "Paused One",
        paused_at: new Date().toISOString(),
      }),
    ]);
    renderActivity("/admin/activity?view=preparations");

    expect(screen.getByRole("button", { name: /Paused\s*1/ })).toBeInTheDocument();
    expect(screen.queryAllByRole("button", { name: "Pause Paused One" })).toHaveLength(0);
    fireEvent.click(screen.getAllByRole("button", { name: "Pause Example Movie" })[0]!);
    expect(mocks.preparationAction).toHaveBeenLastCalledWith(
      { action: "pause", ids: ["art-1"] },
      expect.anything(),
    );
    expect(mocks.toastSuccess).toHaveBeenLastCalledWith("Paused 1 job.");

    fireEvent.click(screen.getAllByRole("button", { name: "Resume Paused One" })[0]!);
    expect(mocks.preparationAction).toHaveBeenLastCalledWith(
      { action: "resume", ids: ["art-p"] },
      expect.anything(),
    );

    // Cancel asks first and names what the requester loses.
    fireEvent.click(screen.getAllByRole("button", { name: "Cancel Example Movie" })[0]!);
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText(/Encoding stops and its progress is lost/)).toBeInTheDocument();
    expect(within(dialog).getByText(/1 waiting download fails/)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel job" }));
    expect(mocks.preparationAction).toHaveBeenLastCalledWith(
      { action: "cancel", ids: ["art-1"] },
      expect.anything(),
    );
  });

  it("acts on the selected jobs in bulk", () => {
    mocks.preparationAction.mockReset();
    mocks.preparations = makePreparationList([
      makePreparation(),
      makePreparation({
        id: "art-q",
        state: "queued",
        progress: undefined,
        media_title: "Queued One",
      }),
      makePreparation({
        id: "art-f",
        state: "failed",
        progress: undefined,
        media_title: "Failed One",
        requesters: [],
      }),
    ]);
    renderActivity("/admin/activity?view=preparations");

    fireEvent.click(screen.getByRole("checkbox", { name: "Select all shown jobs" }));
    const toolbar = screen.getByRole("toolbar", { name: "Selected preparation jobs" });
    expect(within(toolbar).getByText("3 selected")).toBeInTheDocument();
    expect(within(toolbar).getByRole("button", { name: /Resume/ })).toBeDisabled();

    // A failed job cannot be paused, so it is left out of the pause.
    fireEvent.click(within(toolbar).getByRole("button", { name: /Pause 2/ }));
    expect(mocks.preparationAction).toHaveBeenLastCalledWith(
      { action: "pause", ids: ["art-1", "art-q"] },
      expect.anything(),
    );

    fireEvent.click(within(toolbar).getByRole("button", { name: "Cancel 3" }));
    const dialog = screen.getByRole("alertdialog");
    expect(within(dialog).getByText("Cancel 3 jobs?")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel 3 jobs" }));
    expect(mocks.preparationAction).toHaveBeenLastCalledWith(
      { action: "cancel", ids: ["art-1", "art-q", "art-f"] },
      expect.anything(),
    );

    fireEvent.click(within(toolbar).getByRole("button", { name: "Clear selection" }));
    expect(
      screen.queryByRole("toolbar", { name: "Selected preparation jobs" }),
    ).not.toBeInTheDocument();
  });

  it("acts only on selected jobs the filters show", () => {
    mocks.preparationAction.mockReset();
    mocks.preparations = makePreparationList([
      makePreparation(),
      makePreparation({
        id: "art-q",
        state: "queued",
        progress: undefined,
        media_title: "Queued One",
      }),
    ]);
    renderActivity("/admin/activity?view=preparations");

    fireEvent.click(screen.getByRole("checkbox", { name: "Select all shown jobs" }));
    fireEvent.click(screen.getByRole("button", { name: /Queued\s*1/ }));
    const toolbar = screen.getByRole("toolbar", { name: "Selected preparation jobs" });
    expect(within(toolbar).getByText("1 selected")).toBeInTheDocument();
    fireEvent.click(within(toolbar).getByRole("button", { name: /Pause 1/ }));
    expect(mocks.preparationAction).toHaveBeenLastCalledWith(
      { action: "pause", ids: ["art-q"] },
      expect.anything(),
    );
  });

  it("refreshes streams and preparations together", () => {
    mocks.preparations = makePreparationList([]);
    renderActivity("/admin/activity?view=preparations");
    fireEvent.click(screen.getByRole("button", { name: /Refresh/ }));
    expect(mocks.refresh).toHaveBeenCalled();
    expect(mocks.preparationsRefetch).toHaveBeenCalled();
    expect(screen.getByText("No downloads being prepared")).toBeInTheDocument();
  });
});
