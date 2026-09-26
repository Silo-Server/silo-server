import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, waitFor } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { joinWatchTogetherRoom } from "@/lib/watchTogether";
import WatchPartyHub from "@/pages/watchtogether/WatchPartyHub";
const state = vi.hoisted(() => ({ token: "invite-A", navigate: vi.fn() }));
vi.mock("react-router", () => ({
  useSearchParams: () => [new URLSearchParams({ token: state.token })],
}));
vi.mock("@/hooks/useViewTransition", () => ({ useViewTransitionNavigate: () => state.navigate }));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/useAuth", () => ({ useOptionalAuth: () => null }));
const snapshot = {
  room_id: "room",
  phase: "playing",
  playback_state: "paused",
  selection_mode: "host_pick",
  selection_revision: 2,
  selected_file_id: "7",
  selected_library_id: "8",
  code: "ROOM",
  guest_control_policy: "host_only",
  is_paused: true,
  anchor_position_seconds: 3,
  anchor_updated_at: "2026-01-01T00:00:00Z",
  generation: 4,
  member_count: 1,
  host_connected: true,
  self_role: "host",
  self_can_control_transport: true,
  self_can_manage_room: true,
  self_ignore_wait: false,
  members: [
    {
      user_id: "1",
      profile_id: "host",
      display_name: "Host",
      is_host: true,
      is_self: true,
      connected: true,
    },
  ],
};
const response = () =>
  new Response(JSON.stringify({ room: snapshot, room_access_token: "renewed-proof" }), {
    headers: { "Content-Type": "application/json" },
  });

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("login");
  setProfileId("host");
  setProfileToken(null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("sends original join input once and normalizes room proof", async () => {
  const fetch = vi.fn().mockResolvedValue(response());
  vi.stubGlobal("fetch", fetch);
  const result = await joinWatchTogetherRoom({ code: "ROOM", join_token: "invite" });
  expect(result.room.room_id).toBe("room");
  expect(result.room_access_token).toBe("renewed-proof");
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/join");
  expect(JSON.parse(fetch.mock.calls[0]![1].body)).toEqual({ code: "ROOM", join_token: "invite" });
});
it.each([401, 403, 404, 409, 422, 500])("does not replay join %s", async (status) => {
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
  vi.stubGlobal("fetch", fetch);
  await expect(joinWatchTogetherRoom({ code: "ROOM" })).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
it.each(["invite", "authority", "unmount"])(
  "mounted join refuses late receipt after %s replacement",
  async (kind) => {
    state.token = "invite-A";
    state.navigate.mockClear();
    const pending: Array<(r: Response) => void> = [];
    const fetch = vi
      .fn()
      .mockImplementation(() => new Promise<Response>((resolve) => pending.push(resolve)));
    vi.stubGlobal("fetch", fetch);
    const view = render(<WatchPartyHub />);
    await waitFor(() => expect(pending.length).toBe(1));
    if (kind === "invite") {
      state.token = "invite-B";
      view.rerender(<WatchPartyHub />);
      await waitFor(() => expect(pending.length).toBe(2));
    }
    if (kind === "authority") setProfileToken("replacement");
    if (kind === "unmount") view.unmount();
    await act(async () => {
      pending[0]!(response());
    });
    expect(state.navigate).not.toHaveBeenCalled();
    if (kind === "invite") {
      expect(view.queryByLabelText("Room code")).toBeNull();
      await act(async () => {
        pending[1]!(response());
      });
      expect(state.navigate).toHaveBeenCalledTimes(1);
      expect(state.navigate).toHaveBeenCalledWith("/rooms/room?room_token=renewed-proof", {
        replace: true,
      });
    }
  },
);

it.each([
  ["invite removal", 200],
  ["invite removal", 500],
  ["authority replacement", 200],
  ["authority replacement", 500],
] as const)(
  "releases owned busy state after %s and stale %s without navigation",
  async (kind, status) => {
    state.token = "invite-A";
    state.navigate.mockClear();
    const pending: Array<(r: Response) => void> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => new Promise<Response>((resolve) => pending.push(resolve))),
    );
    const view = render(<WatchPartyHub />);
    await waitFor(() => expect(pending.length).toBe(1));
    if (kind === "invite removal") state.token = "";
    else setProfileToken("replacement");
    view.rerender(<WatchPartyHub />);
    const code = view.getByLabelText("Room code");
    expect(code).toBeEnabled();
    fireEvent.change(code, { target: { value: "ROOM" } });
    expect(view.getByRole("button", { name: "Join Watch Party" })).toBeEnabled();
    await act(async () => {
      pending[0]!(status === 200 ? response() : new Response(null, { status }));
    });
    expect(state.navigate).not.toHaveBeenCalled();
    expect(view.getByRole("button", { name: "Join Watch Party" })).toBeEnabled();
  },
);
