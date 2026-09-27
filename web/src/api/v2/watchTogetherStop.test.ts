import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { stopRoomPlayback } from "./watchTogetherStop";

const room = {
  room_id: "room",
  phase: "lobby",
  playback_state: "idle",
  selection_mode: "host_pick",
  selection_revision: 2,
  selected_content_id: "dune",
  code: "KX7Q2M",
  guest_control_policy: "host_only",
  is_paused: true,
  anchor_position_seconds: 0,
  anchor_updated_at: "2026-01-01T00:00:00Z",
  generation: 5,
  member_count: 2,
  host_connected: true,
  self_role: "host",
  self_can_control_transport: true,
  self_can_manage_room: true,
  self_ignore_wait: false,
};

beforeEach(() => {
  setAccessToken("login");
  setProfileId("p1");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

describe("stopRoomPlayback", () => {
  it("posts once with the room path and returns the lobby snapshot", async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ room, room_access_token: "renewed" }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetch);
    const result = await stopRoomPlayback("room");
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(String(fetch.mock.calls[0]![0])).toMatch(
      /\/api\/v2\/watch-together\/rooms\/room\/playback\/stop$/,
    );
    expect(fetch.mock.calls[0]![1].method).toBe("POST");
    expect(result.room.phase).toBe("lobby");
    expect(result.room.selected_content_id).toBe("dune");
    expect(result.room_access_token).toBe("renewed");
  });

  it("aborts silently when the profile authority is replaced mid-flight", async () => {
    let resolve!: (r: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => new Promise<Response>((r) => (resolve = r))),
    );
    const pending = stopRoomPlayback("room");
    setProfileToken("replacement");
    resolve(
      new Response(JSON.stringify({ room, room_access_token: "renewed" }), {
        headers: { "Content-Type": "application/json" },
      }),
    );
    await expect(pending).rejects.toMatchObject({ name: "StaleApiRequestContextError" });
  });
});
