import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useWatchTogetherRoomConnection } from "@/player/hooks/useWatchTogetherRoomConnection";
import { queryRoomMemberState } from "./watchTogetherMemberState";
import { readRoomPicker } from "./watchTogetherPicker";

const snapshot = {
  room_id: "room",
  phase: "lobby",
  playback_state: "idle",
  selection_mode: "host_pick",
  selection_revision: 0,
  selected_content_id: "movie",
  code: "ROOM",
  guest_control_policy: "host_only",
  is_paused: true,
  anchor_position_seconds: 0,
  anchor_updated_at: "2026-01-01T00:00:00Z",
  generation: 2,
  member_count: 2,
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
    {
      user_id: "2",
      profile_id: "guest",
      display_name: "Guest",
      is_host: false,
      is_self: false,
      connected: true,
      lobby_ready: true,
    },
  ],
};
const roomResponse = (overrides: Record<string, unknown> = {}) =>
  new Response(
    JSON.stringify({ room: { ...snapshot, ...overrides }, room_access_token: "renewed-proof" }),
    {
      headers: { "Content-Type": "application/json" },
    },
  );

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

it("stages an item with string IDs and keeps lobby_ready from the receipt", async () => {
  const fetch = vi.fn().mockResolvedValue(roomResponse());
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: null }),
  );
  await act(async () => {
    const room = await result.current.stageItem({ content_id: "movie", file_id: 7 });
    expect(room?.phase).toBe("lobby");
    expect(room?.members?.[1]?.lobby_ready).toBe(true);
    expect(room?.members?.[0]?.lobby_ready).toBe(false);
  });
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms/room/staged-selection");
  expect(fetch.mock.calls[0]![1].method).toBe("PUT");
  expect(JSON.parse(fetch.mock.calls[0]![1].body)).toEqual({ content_id: "movie", file_id: "7" });
});

it("starts playback with no body and switches mode with the enum", async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(
      roomResponse({
        phase: "playing",
        playback_state: "waiting",
        selection_revision: 1,
        generation: 3,
      }),
    )
    .mockResolvedValueOnce(
      roomResponse({ selection_mode: "vote", selected_content_id: undefined, generation: 4 }),
    );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: null }),
  );
  await act(async () => {
    const started = await result.current.startPlayback();
    expect(started?.phase).toBe("playing");
    expect(started?.selection_revision).toBe(1);
  });
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms/room/playback/start");
  expect(fetch.mock.calls[0]![1].method).toBe("POST");
  expect(fetch.mock.calls[0]![1].body).toBeUndefined();
  await act(async () => {
    const switched = await result.current.updateSelectionMode("vote");
    expect(switched?.selection_mode).toBe("vote");
    expect(switched?.selected_content_id).toBeUndefined();
    expect(switched?.generation).toBe(4);
  });
  expect(fetch.mock.calls[1]![0]).toBe("/api/v2/watch-together/rooms/room/selection-mode");
  expect(fetch.mock.calls[1]![1].method).toBe("PATCH");
  expect(JSON.parse(fetch.mock.calls[1]![1].body)).toEqual({ selection_mode: "vote" });
});

it.each([403, 409, 422])("does not publish a %s lobby refusal", async (status) => {
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: null }),
  );
  await act(async () => {
    await expect(result.current.startPlayback()).rejects.toThrow();
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(result.current.room).toBeNull();
});

it("lobby ready is a socket frame and reports when no socket is open", () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 500 })));
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: null }),
  );
  expect(result.current.setLobbyReady(true)).toEqual({ ok: false });
});

it("member-state sends proof, dedupes ids and converts member identities", async () => {
  const fetch = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        members: snapshot.members,
        items: [
          {
            content_id: "movie",
            members: [
              {
                user_id: "1",
                profile_id: "host",
                state: "in_progress",
                position_seconds: 12,
                on_watchlist: false,
              },
              { user_id: "2", profile_id: "guest", state: "unseen", on_watchlist: true },
            ],
          },
        ],
      }),
      { headers: { "Content-Type": "application/json" } },
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const out = await queryRoomMemberState("room", "proof", ["movie", " movie ", "", "movie"]);
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms/room/member-state");
  expect(new Headers(fetch.mock.calls[0]![1].headers).get("X-Room-Token")).toBe("proof");
  expect(JSON.parse(fetch.mock.calls[0]![1].body)).toEqual({ content_ids: ["movie"] });
  expect(out.items[0]?.members[0]).toEqual({
    user_id: 1,
    profile_id: "host",
    state: "in_progress",
    position_seconds: 12,
    duration_seconds: undefined,
    on_watchlist: false,
  });
  expect(out.items[0]?.members[1]?.user_id).toBe(2);
});

it("member-state with no ids makes no request and refuses oversized sets", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  await expect(queryRoomMemberState("room", "proof", ["", " "])).resolves.toEqual({
    members: [],
    items: [],
  });
  await expect(
    queryRoomMemberState(
      "room",
      "proof",
      Array.from({ length: 201 }, (_, i) => `id-${i}`),
    ),
  ).rejects.toThrow(/limited to 200/);
  expect(fetch).not.toHaveBeenCalled();
});

it("picker sends proof and converts member identities on both rows", async () => {
  const entry = {
    item: {
      content_id: "severance",
      type: "series",
      title: "Severance",
      genres: [],
      keywords: [],
      status: "matched",
    },
    members: [{ user_id: "1", profile_id: "host", display_name: "Host", position_seconds: 600 }],
    next_up: {
      content_id: "s2e4",
      season_number: 2,
      episode_number: 4,
      title: "Woe's Hollow",
      member_count: 2,
    },
  };
  const fetch = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        members: snapshot.members,
        continue_together: [entry],
        watchlist_union: [],
      }),
      {
        headers: { "Content-Type": "application/json" },
      },
    ),
  );
  vi.stubGlobal("fetch", fetch);
  const out = await readRoomPicker("room", "proof");
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms/room/picker");
  expect(new Headers(fetch.mock.calls[0]![1].headers).get("X-Room-Token")).toBe("proof");
  expect(out.continue_together[0]?.members[0]?.user_id).toBe(1);
  expect(out.continue_together[0]?.next_up?.member_count).toBe(2);
  expect(out.watchlist_union).toEqual([]);
});
