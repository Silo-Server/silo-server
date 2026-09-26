import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import {
  setAccessToken,
  setProfileId,
  setProfileToken,
  StaleApiRequestContextError,
} from "@/api/client";
import { getWatchTogetherRoom } from "@/lib/watchTogether";
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
it("reads with header proof and converts exact snapshot identities", async () => {
  const fetch = vi.fn().mockResolvedValue(response());
  vi.stubGlobal("fetch", fetch);
  const result = await getWatchTogetherRoom("room", "original-proof");
  expect(result.room.selected_file_id).toBe(7);
  expect(result.room.members?.[0]?.user_id).toBe(1);
  expect(result.room_access_token).toBe("renewed-proof");
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms/room");
  expect(new Headers(fetch.mock.calls[0]![1].headers).get("X-Room-Token")).toBe("original-proof");
});
it("refuses unsafe identities and replacement-authority success or failure", async () => {
  const fetch = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        room: { ...snapshot, selected_file_id: "9007199254740992" },
        room_access_token: "proof",
      }),
      { headers: { "Content-Type": "application/json" } },
    ),
  );
  vi.stubGlobal("fetch", fetch);
  await expect(getWatchTogetherRoom("room", "proof")).rejects.toThrow();
  fetch.mockImplementation(async () => {
    setProfileToken("new");
    return response();
  });
  await expect(getWatchTogetherRoom("room", "proof")).rejects.toThrow();
  fetch.mockImplementation(async () => {
    setProfileToken("next");
    return new Response(null, { status: 409 });
  });
  await expect(getWatchTogetherRoom("room", "proof")).rejects.toBeInstanceOf(
    StaleApiRequestContextError,
  );
});
it("mounted room read treats closed as terminal", async () => {
  class Socket extends EventTarget {
    static OPEN = 1;
    readyState = 0;
    close() {}
    send() {}
  }
  vi.stubGlobal("WebSocket", Socket);
  const fetch = vi.fn().mockImplementation(async (url: string) =>
    String(url).endsWith("/suggestions?limit=100")
      ? new Response(JSON.stringify({ items: [], page: { has_more: false } }), {
          headers: { "Content-Type": "application/json" },
        })
      : new Response(
          JSON.stringify({
            type: "https://siloserver.org/docs/api/v2/problems/conflict",
            title: "Conflict",
            status: 409,
            detail: "Room closed.",
          }),
          { status: 409, headers: { "Content-Type": "application/problem+json" } },
        ),
  );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: "proof" }),
  );
  await waitFor(() => expect(result.current.closedReason).toBe("ended"));
  expect(result.current.room).toBeNull();
});
it("mounted old room read cannot publish after room replacement", async () => {
  class Socket extends EventTarget {
    static OPEN = 1;
    readyState = 0;
    close() {}
    send() {}
  }
  vi.stubGlobal("WebSocket", Socket);
  let resolve!: (r: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation(async (url: string) =>
      String(url) === "/api/v2/watch-together/rooms/room"
        ? new Promise<Response>((r) => {
            resolve = r;
          })
        : new Response(JSON.stringify({ items: [], page: { has_more: false } }), {
            headers: { "Content-Type": "application/json" },
          }),
    ),
  );
  const { result, rerender } = renderHook(
    ({ id }: { id: string | null }) =>
      useWatchTogetherRoomConnection({ roomId: id, roomToken: "proof" }),
    { initialProps: { id: "room" as string | null } },
  );
  await waitFor(() => expect(resolve).toBeDefined());
  rerender({ id: null });
  await act(async () => {
    resolve(response());
  });
  expect(result.current.room).toBeNull();
});
