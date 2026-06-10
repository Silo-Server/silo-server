import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  invalidateMediaSurfaceQueries: vi.fn(),
  toastSuccess: vi.fn(),
  useMutation: vi.fn(),
  useQueryClient: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");

  return {
    ...actual,
    useMutation: (...args: unknown[]) => mocks.useMutation(...args),
    useQueryClient: () => mocks.useQueryClient(),
  };
});

vi.mock("@/api/client", () => ({
  api: mocks.api,
}));

vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ awaitAdminJob: vi.fn() }),
}));

vi.mock("@/pages/ItemDetail/watchedState", async () => {
  const actual = await vi.importActual<typeof import("@/pages/ItemDetail/watchedState")>(
    "@/pages/ItemDetail/watchedState",
  );

  return {
    ...actual,
    getCachedWatchedInvalidationKeys: vi.fn(() => []),
  };
});

vi.mock("./mediaSurfaceRefresh", () => ({
  invalidateMediaSurfaceQueries: (...args: unknown[]) =>
    mocks.invalidateMediaSurfaceQueries(...args),
}));

vi.mock("@/pages/homeSurfaceRefresh", () => ({
  bumpHomeRefreshSignal: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
  },
}));

import { fetchWatchDetail, redetectEpisodeIntro, useWatchedStateMutation } from "./items";

type WatchedMutationOptions = {
  mutationFn: (nextPlayed: boolean) => Promise<unknown>;
  onSuccess?: (data: unknown, nextPlayed: boolean) => void;
  onSettled?: () => Promise<unknown>;
};

describe("item query helpers", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.api.mockResolvedValue({});
    mocks.invalidateMediaSurfaceQueries.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.useMutation.mockReset();
    mocks.useMutation.mockImplementation((options: unknown) => ({
      ...(options as object),
      mutate: vi.fn(),
    }));
    mocks.useQueryClient.mockReset();
    mocks.useQueryClient.mockReturnValue({});
  });

  it("encodes item IDs in watch detail endpoints", async () => {
    await fetchWatchDetail("ebook 1/isbn:978", 42, 12);

    expect(mocks.api).toHaveBeenCalledWith(
      "/watch/ebook%201%2Fisbn%3A978?fileId=42&library_id=12",
      undefined,
    );
  });

  it("encodes item IDs in admin item endpoints", async () => {
    await redetectEpisodeIntro("episode 1/id:abc");

    expect(mocks.api).toHaveBeenCalledWith("/admin/items/episode%201%2Fid%3Aabc/redetect-intro", {
      method: "POST",
    });
  });

  it("toggles ebook read state through the watched endpoint", async () => {
    useWatchedStateMutation({ content_id: "ebook 1/isbn:978", type: "ebook" });
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as WatchedMutationOptions;

    await options.mutationFn(true);
    expect(mocks.api).toHaveBeenCalledWith("/watched/ebook%201%2Fisbn%3A978", { method: "POST" });

    await options.mutationFn(false);
    expect(mocks.api).toHaveBeenCalledWith("/watched/ebook%201%2Fisbn%3A978", {
      method: "DELETE",
    });
  });

  it("uses read toast copy and refreshes surfaces for ebook watched toggles", async () => {
    useWatchedStateMutation({ content_id: "ebook-1", type: "ebook" });
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as WatchedMutationOptions;

    options.onSuccess?.(undefined, true);
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Marked as read");

    options.onSuccess?.(undefined, false);
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Marked as unread");

    await options.onSettled?.();
    expect(mocks.invalidateMediaSurfaceQueries).toHaveBeenCalledWith(expect.anything(), {
      itemId: "ebook-1",
      watchedKeys: [],
    });
  });
});
