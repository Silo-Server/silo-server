import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { act, cleanup, renderHook } from "@testing-library/react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useWatchTogetherRoomConnection } from "@/player/hooks/useWatchTogetherRoomConnection";
import { endWatchTogetherRoom } from "@/lib/watchTogetherActions";
import { toast } from "sonner";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("login");
  setProfileId("host");
  setProfileToken(null);
  vi.clearAllMocks();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("closes from the mounted room action with one exact DELETE and no room proof", async () => {
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room-one", roomToken: null }),
  );
  await act(async () => {
    await endWatchTogetherRoom(result.current.closeRoom);
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0]![0]).toBe("/api/v2/watch-together/rooms/room-one");
  expect(fetch.mock.calls[0]![1].method).toBe("DELETE");
  expect(new Headers(fetch.mock.calls[0]![1].headers).has("X-Room-Token")).toBe(false);
  expect(toast.success).toHaveBeenCalledWith("Room ended");
});
it.each([401, 403, 404, 409, 500])("does not retry %s or claim success", async (status) => {
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room-one", roomToken: null }),
  );
  await act(async () => {
    await endWatchTogetherRoom(result.current.closeRoom);
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(toast.success).not.toHaveBeenCalled();
  expect(toast.error).toHaveBeenCalledTimes(1);
});
it("refuses stale render authority and suppresses stale receipt feedback", async () => {
  const fetch = vi.fn().mockImplementation(async () => {
    setProfileToken("later");
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fetch);
  const { result, rerender } = renderHook(() =>
    useWatchTogetherRoomConnection({ roomId: "room-one", roomToken: null }),
  );
  const original = result.current.closeRoom;
  setProfileToken("replacement");
  rerender();
  await act(async () => {
    await expect(original()).rejects.toThrow();
  });
  expect(fetch).not.toHaveBeenCalled();
  await act(async () => {
    await endWatchTogetherRoom(original);
  });
  expect(toast.error).not.toHaveBeenCalled();
  await act(async () => {
    await endWatchTogetherRoom(result.current.closeRoom);
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(toast.success).not.toHaveBeenCalled();
  expect(toast.error).not.toHaveBeenCalled();
});
