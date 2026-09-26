import { afterEach, expect, it, vi } from "vitest";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { PropsWithChildren } from "react";
import type { WatchTogetherRoomMember } from "@/lib/watchTogether";
import { getWatchTogetherRoomPicker, queryWatchTogetherMemberState } from "@/lib/watchTogether";
import { useMemberState, usePickerRows } from "./usePickerData";

vi.mock("@/lib/watchTogether", () => ({
  getWatchTogetherRoomPicker: vi.fn(async () => ({
    members: [],
    continue_together: [],
    watchlist_union: [],
  })),
  queryWatchTogetherMemberState: vi.fn(async () => ({ members: [], items: [] })),
}));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

it("reloads picker data when a different member replaces one without changing the count", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  function wrapper({ children }: PropsWithChildren) {
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  }
  const member = (id: number): WatchTogetherRoomMember => ({
    user_id: id,
    profile_id: `p${id}`,
    display_name: `Member ${id}`,
    is_host: false,
    is_self: false,
    connected: true,
  });
  const { rerender } = renderHook(
    ({ members }) => {
      usePickerRows("room", "proof", members);
      useMemberState("room", "proof", members, ["movie"]);
    },
    { wrapper, initialProps: { members: [member(1), member(2)] } },
  );
  await waitFor(() => expect(queryWatchTogetherMemberState).toHaveBeenCalledTimes(1));
  rerender({ members: [member(1), member(3)] });
  await waitFor(() => expect(getWatchTogetherRoomPicker).toHaveBeenCalledTimes(2));
  expect(queryWatchTogetherMemberState).toHaveBeenCalledTimes(2);
  cleanup();
  client.clear();
});
