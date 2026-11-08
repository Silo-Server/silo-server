import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import type { WatchTogetherJoinRequest, WatchTogetherPublicRoom } from "@/lib/watchTogether";
import { usePublicRooms } from "./usePublicRooms";

const api = vi.hoisted(() => ({
  listPublicRooms: vi.fn(),
  readJoinRequest: vi.fn(),
  createJoinRequest: vi.fn(),
}));
vi.mock("@/api/v2/watchTogetherPublicRooms", () => api);

const request = (status: WatchTogetherJoinRequest["status"]): WatchTogetherJoinRequest => ({
  id: "req",
  room_id: "room",
  user_id: 2,
  profile_id: "guest",
  display_name: "Guest",
  status,
  created_at: "2026-11-07T00:00:00.000Z",
});
const party = (status?: WatchTogetherJoinRequest["status"]): WatchTogetherPublicRoom => ({
  room_id: "room",
  host_user_id: 1,
  host_profile_id: "host",
  host_display_name: "Host",
  host_connected: true,
  phase: "lobby",
  playback_state: "idle",
  selection_mode: "host_pick",
  member_count: 1,
  created_at: "2026-11-07T00:00:00.000Z",
  is_host: false,
  join_request: status ? request(status) : undefined,
});
const admitted = {
  request: request("approved"),
  room: { room: { room_id: "room" }, room_access_token: "proof" },
};

beforeEach(() => {
  vi.useFakeTimers();
  setAccessToken("login");
  setProfileId("guest");
  setProfileToken(null);
  api.listPublicRooms.mockReset();
  api.readJoinRequest.mockReset();
});
afterEach(() => {
  vi.useRealTimers();
});

describe("usePublicRooms", () => {
  it.each([
    ["the list already shows the approval", () => [party("approved")]],
    ["the host unlisted the room", () => []],
  ])("still lets a waiting requester in when %s", async (_case, nextList) => {
    // The host answers between list reads: request polls before the second
    // list read still see pending, and only later ones see the approval.
    let listReads = 0;
    api.listPublicRooms.mockImplementation(async () =>
      ++listReads === 1 ? [party("pending")] : nextList(),
    );
    api.readJoinRequest.mockImplementation(async () =>
      listReads >= 2 ? admitted : { request: request("pending"), room: null },
    );
    const onApproved = vi.fn();
    renderHook(() => usePublicRooms({ profileKey: "2:guest", onApproved }));

    // First read: the request is pending, so the page waits on it.
    await act(() => vi.advanceTimersByTimeAsync(0));
    // The next list read changes the row before the request poll runs.
    await act(() => vi.advanceTimersByTimeAsync(10_000));
    expect(api.listPublicRooms).toHaveBeenCalledTimes(2);
    expect(onApproved).not.toHaveBeenCalled();

    await act(() => vi.advanceTimersByTimeAsync(3_000));
    expect(onApproved).toHaveBeenCalledWith(admitted.room);
  });

  it("keeps looking after the server said public rooms were unavailable", async () => {
    const { V2ProblemError } = await import("@/api/v2/request");
    api.listPublicRooms
      .mockRejectedValueOnce(
        Object.assign(Object.create(V2ProblemError.prototype), { status: 503 }),
      )
      .mockResolvedValue([party()]);
    const { result } = renderHook(() =>
      usePublicRooms({ profileKey: "2:guest", onApproved: vi.fn() }),
    );
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(result.current.unavailable).toBe(true);

    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(result.current.unavailable).toBe(false);
    expect(result.current.rooms).toHaveLength(1);
  });
});
