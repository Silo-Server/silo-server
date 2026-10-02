import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useWatchTogetherRoomConnection } from "@/player/hooks/useWatchTogetherRoomConnection";
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
it("sends exact selection once with string IDs and uses the authoritative receipt", async () => {
  const fetch = vi.fn().mockResolvedValue(response());
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: null }),
  );
  await act(async () => {
    const room = await result.current.selectItem({
      content_id: "movie",
      file_id: 7,
      library_id: 8,
    });
    expect(room?.selected_file_id).toBe(7);
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms/room/selection");
  expect(fetch.mock.calls[0]![1].method).toBe("PUT");
  expect(JSON.parse(fetch.mock.calls[0]![1].body)).toEqual({
    content_id: "movie",
    file_id: "7",
    library_id: "8",
  });
  expect(new Headers(fetch.mock.calls[0]![1].headers).has("X-Room-Token")).toBe(false);
});
it.each([401, 403, 409, 422, 500])("does not replay %s or publish selection", async (status) => {
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: null }),
  );
  await act(async () => {
    await expect(result.current.selectItem({ content_id: "movie" })).rejects.toThrow();
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(result.current.room).toBeNull();
});
it.each([200, 500])("refuses old authority and replaced-room %s receipts", async (status) => {
  let resolve!: (r: Response) => void;
  const fetch = vi.fn().mockImplementation(
    () =>
      new Promise<Response>((r) => {
        resolve = r;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const { result, rerender } = renderHook(
    ({ id }: { id: string }) => useWatchTogetherRoomConnection({ roomId: id, roomToken: null }),
    { initialProps: { id: "room" } },
  );
  const old = result.current.selectItem;
  setProfileToken("replacement");
  rerender({ id: "room" });
  await act(async () => {
    await expect(old({ content_id: "movie" })).rejects.toThrow();
  });
  expect(fetch).not.toHaveBeenCalled();
  let done!: ReturnType<typeof result.current.selectItem>;
  act(() => {
    done = result.current.selectItem({ content_id: "movie" });
  });
  rerender({ id: "other" });
  await act(async () => {
    resolve(status === 200 ? response() : new Response(null, { status }));
    expect(await done).toBeNull();
  });
  expect(result.current.room).toBeNull();
});
it("refuses unsafe legacy UI IDs without dispatch", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: null }),
  );
  await act(async () => {
    await expect(
      result.current.selectItem({ content_id: "movie", file_id: Number.MAX_SAFE_INTEGER + 1 }),
    ).rejects.toThrow();
  });
  expect(fetch).not.toHaveBeenCalled();
});

it.each(["room", "other"])(
  "compares selection generations only within room %s",
  async (targetRoom) => {
    class Socket extends EventTarget {
      static OPEN = 1;
      readyState = 0;
      close() {}
      send() {}
    }
    vi.stubGlobal("WebSocket", Socket);
    const roomResponse = (roomId: string, generation: number) =>
      new Response(
        JSON.stringify({
          room: { ...snapshot, room_id: roomId, generation },
          room_access_token: "proof",
        }),
        { headers: { "Content-Type": "application/json" } },
      );
    const fetch = vi.fn().mockImplementation((url: string) => {
      if (url.endsWith("/suggestions?limit=100"))
        return Promise.resolve(
          new Response(JSON.stringify({ items: [], page: { has_more: false } }), {
            headers: { "Content-Type": "application/json" },
          }),
        );
      if (url.endsWith("/selection")) return Promise.resolve(roomResponse(targetRoom, 2));
      if (url === "/api/v2/watch-together/rooms/room")
        return Promise.resolve(roomResponse("room", 20));
      if (url === "/api/v2/watch-together/rooms/other") return new Promise<Response>(() => {});
      throw new Error(`Unexpected request: ${url}`);
    });
    vi.stubGlobal("fetch", fetch);
    const { result, rerender } = renderHook(
      ({ id }: { id: string }) =>
        useWatchTogetherRoomConnection({ roomId: id, roomToken: "proof" }),
      { initialProps: { id: "room" } },
    );
    await waitFor(() => expect(result.current.room?.generation).toBe(20));
    rerender({ id: targetRoom });
    if (targetRoom === "other") {
      await waitFor(() =>
        expect(fetch.mock.calls.some(([url]) => url === "/api/v2/watch-together/rooms/other")).toBe(
          true,
        ),
      );
    }
    await act(async () => {
      const receipt = await result.current.selectItem({ content_id: "movie" });
      expect(receipt?.room_id).toBe(targetRoom);
      expect(receipt?.generation).toBe(2);
    });
    expect(result.current.room?.room_id).toBe(targetRoom);
    expect(result.current.room?.generation).toBe(targetRoom === "room" ? 20 : 2);
  },
);
