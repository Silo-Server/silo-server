import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { PropsWithChildren } from "react";
import { ApiClientError, StaleApiRequestContextError } from "@/api/client";
import { getWatchTogetherRoom } from "@/lib/watchTogether";
import { markRecentRoomEnded } from "./useRecentRooms";
import { useLiveRoomPresence } from "./useLiveRoomPresence";

const recent = vi.hoisted(() => [
  {
    user_id: 1,
    profile_id: "profile",
    room_id: "room",
    code: "CODE",
    token: "proof",
    role: "host",
    last_seen_at: new Date().toISOString(),
  },
]);
vi.mock("./useRecentRooms", () => ({ useRecentRooms: () => recent, markRecentRoomEnded: vi.fn() }));
vi.mock("@/lib/watchTogether", () => ({ getWatchTogetherRoom: vi.fn() }));

let client: QueryClient;
beforeEach(() => {
  vi.clearAllMocks();
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
});
afterEach(() => {
  cleanup();
  client.clear();
});

function wrapper({ children }: PropsWithChildren) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

it.each([new TypeError("network unavailable"), new StaleApiRequestContextError()])(
  "keeps a recent room after a transient or stale-authority error: %s",
  async (error) => {
    vi.mocked(getWatchTogetherRoom).mockRejectedValueOnce(error);
    renderHook(() => useLiveRoomPresence(), { wrapper });
    await waitFor(() => expect(client.isFetching()).toBe(0));
    expect(getWatchTogetherRoom).toHaveBeenCalledOnce();
    expect(markRecentRoomEnded).not.toHaveBeenCalled();
  },
);

it("marks a room ended after a definitive not-found response", async () => {
  vi.mocked(getWatchTogetherRoom).mockRejectedValueOnce(
    new ApiClientError(404, "not_found", "Room not found"),
  );
  renderHook(() => useLiveRoomPresence(), { wrapper });
  await waitFor(() => expect(markRecentRoomEnded).toHaveBeenCalledWith(recent[0]));
});
