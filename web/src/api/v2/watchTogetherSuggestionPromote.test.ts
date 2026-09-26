import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useWatchTogetherRoomConnection } from "@/player/hooks/useWatchTogetherRoomConnection";
import { promoteWatchTogetherWithFeedback } from "@/lib/watchTogetherActions";
import { toast } from "sonner";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
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
  vi.clearAllMocks();
  class Socket extends EventTarget {
    static OPEN = 1;
    readyState = 0;
    close() {}
    send() {}
  }
  vi.stubGlobal("WebSocket", Socket);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function transport(post: () => Promise<Response>) {
  const fetch = vi.fn().mockImplementation((url: string, init: RequestInit) => {
    if (url.endsWith("/ws-ticket"))
      return Promise.resolve(
        new Response(JSON.stringify({ protocol: "silo.room.v2", ticket: "a".repeat(43) }), {
          headers: { "Content-Type": "application/json" },
        }),
      );
    if (url.endsWith("/suggestions/promote") && init.method === "POST") return post();
    if (url.endsWith("/suggestions?limit=100"))
      return Promise.resolve(
        new Response(JSON.stringify({ items: [], page: { has_more: false } }), {
          headers: { "Content-Type": "application/json" },
        }),
      );
    return new Promise<Response>(() => {});
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}
it("promotes once with header proof and reports the authoritative receipt", async () => {
  const fetch = transport(async () => response());
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: "proof" }),
  );
  await act(async () => {
    await promoteWatchTogetherWithFeedback(result.current.promoteSuggestion, "winner");
  });
  const posts = fetch.mock.calls.filter(
    (c) => c[1].method === "POST" && c[0].endsWith("/suggestions/promote"),
  );
  expect(posts).toHaveLength(1);
  expect(posts[0]![0]).toBe("/api/v2/watch-together/rooms/room/suggestions/promote");
  expect(JSON.parse(posts[0]![1].body)).toEqual({ suggestion_id: "winner" });
  expect(new Headers(posts[0]![1].headers).get("X-Room-Token")).toBe("proof");
  expect(result.current.room?.generation).toBe(4);
  expect(toast.success).toHaveBeenCalledWith("Room selection updated");
});
it.each([401, 403, 409, 422, 500])("does not replay promotion %s", async (status) => {
  const fetch = transport(async () => new Response(null, { status }));
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room", roomToken: "proof" }),
  );
  await act(async () => {
    await promoteWatchTogetherWithFeedback(result.current.promoteSuggestion, "winner");
  });
  expect(
    fetch.mock.calls.filter((c) => c[1].method === "POST" && c[0].endsWith("/suggestions/promote")),
  ).toHaveLength(1);
  expect(result.current.room).toBeNull();
  expect(toast.success).not.toHaveBeenCalled();
  expect(toast.error).toHaveBeenCalledTimes(1);
});
it.each([200, 500])(
  "refuses old authority and replaced-room promotion %s feedback",
  async (status) => {
    let resolve!: (r: Response) => void;
    const fetch = transport(
      () =>
        new Promise<Response>((r) => {
          resolve = r;
        }),
    );
    const { result, rerender } = renderHook(
      ({ id }) => useWatchTogetherRoomConnection({ roomId: id, roomToken: "proof" }),
      { initialProps: { id: "room" } },
    );
    const old = result.current.promoteSuggestion;
    setProfileToken("new");
    rerender({ id: "room" });
    await act(async () => {
      await expect(old("winner")).rejects.toThrow();
    });
    expect(
      fetch.mock.calls.filter(
        (c) => c[1].method === "POST" && c[0].endsWith("/suggestions/promote"),
      ),
    ).toHaveLength(0);
    let done!: Promise<void>;
    act(() => {
      done = promoteWatchTogetherWithFeedback(result.current.promoteSuggestion, "winner");
    });
    rerender({ id: "other" });
    await act(async () => {
      resolve(status === 200 ? response() : new Response(null, { status }));
      await done;
    });
    expect(result.current.room).toBeNull();
    expect(toast.success).not.toHaveBeenCalled();
    expect(toast.error).not.toHaveBeenCalled();
  },
);
it.each(["room", "other"])(
  "compares promotion generations only within room %s",
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
      if (url.endsWith("/ws-ticket"))
        return Promise.resolve(
          new Response(JSON.stringify({ protocol: "silo.room.v2", ticket: "a".repeat(43) }), {
            headers: { "Content-Type": "application/json" },
          }),
        );
      if (url.endsWith("/suggestions?limit=100"))
        return Promise.resolve(
          new Response(JSON.stringify({ items: [], page: { has_more: false } }), {
            headers: { "Content-Type": "application/json" },
          }),
        );
      if (url.endsWith("/suggestions/promote")) return Promise.resolve(roomResponse(targetRoom, 2));
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
      const receipt = await result.current.promoteSuggestion("winner");
      expect(receipt?.room_id).toBe(targetRoom);
      expect(receipt?.generation).toBe(2);
    });
    expect(result.current.room?.room_id).toBe(targetRoom);
    expect(result.current.room?.generation).toBe(targetRoom === "room" ? 20 : 2);
  },
);
