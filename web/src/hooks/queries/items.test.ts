import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  invalidateMediaSurfaceQueries: vi.fn(),
  isItemDetailQueryKey: vi.fn((_queryKey: unknown, _itemId: string) => true),
  updateCatalogItemDetail: vi.fn(),
  toastError: vi.fn(),
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
  isItemDetailQueryKey: (queryKey: unknown, itemId: string) =>
    mocks.isItemDetailQueryKey(queryKey, itemId),
  updateCatalogItemDetail: (...args: unknown[]) => mocks.updateCatalogItemDetail(...args),
}));

vi.mock("@/pages/homeSurfaceRefresh", () => ({
  bumpHomeRefreshSignal: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: {
    error: (...args: unknown[]) => mocks.toastError(...args),
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
  },
}));

import { fetchWatchDetail, redetectEpisodeIntro, useWatchedStateMutation } from "./items";

type WatchedMutationContext = { previous: Array<[readonly unknown[], unknown]> };

type WatchedMutationOptions = {
  mutationFn: (nextPlayed: boolean) => Promise<unknown>;
  onMutate?: (nextPlayed: boolean) => Promise<WatchedMutationContext>;
  onSuccess?: (data: unknown, nextPlayed: boolean) => void;
  onError?: (err: unknown, nextPlayed: boolean, context?: WatchedMutationContext) => void;
  onSettled?: () => Promise<unknown>;
};

describe("item query helpers", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.api.mockResolvedValue({});
    mocks.invalidateMediaSurfaceQueries.mockReset();
    mocks.updateCatalogItemDetail.mockReset();
    mocks.isItemDetailQueryKey.mockReset();
    mocks.isItemDetailQueryKey.mockReturnValue(true);
    mocks.toastError.mockReset();
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
    expect(mocks.api).toHaveBeenCalledWith("/watched/ebook%201%2Fisbn%3A978", {
      method: "POST",
      keepalive: true,
    });

    await options.mutationFn(false);
    expect(mocks.api).toHaveBeenCalledWith("/watched/ebook%201%2Fisbn%3A978", {
      method: "DELETE",
      keepalive: true,
    });
  });

  it("sends watched-state writes with keepalive so they survive tab close", async () => {
    useWatchedStateMutation({ content_id: "series-1", type: "series" });
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as WatchedMutationOptions;

    // Marking a large series expands to every episode server-side; without
    // keepalive the request dies with the document and nothing is marked.
    await options.mutationFn(true);
    expect(mocks.api).toHaveBeenCalledWith("/watched/series-1", {
      method: "POST",
      keepalive: true,
    });
  });

  it("optimistically flips played state and rolls back when the request fails", async () => {
    const setQueryData = vi.fn();
    const previous: Array<[readonly unknown[], unknown]> = [[["items", "detail", "series-1"], {}]];
    mocks.useQueryClient.mockReturnValue({
      cancelQueries: vi.fn().mockResolvedValue(undefined),
      getQueriesData: vi.fn(() => previous),
      setQueryData,
    });

    useWatchedStateMutation({ content_id: "series-1", type: "series" });
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as WatchedMutationOptions;

    const context = await options.onMutate?.(true);
    expect(mocks.updateCatalogItemDetail).toHaveBeenCalledWith(
      expect.anything(),
      "series-1",
      expect.any(Function),
    );

    // The updater must set played without dropping the other user_state flags.
    const updater = mocks.updateCatalogItemDetail.mock.calls[0]?.[2] as (
      detail: Record<string, unknown>,
    ) => Record<string, unknown>;
    expect(
      updater({
        user_data: { played: false },
        user_state: { played: false, is_favorite: true, in_watchlist: false },
      }),
    ).toMatchObject({
      user_data: { played: true },
      user_state: { played: true, is_favorite: true, in_watchlist: false },
    });

    options.onError?.(new Error("boom"), true, context);
    expect(setQueryData).toHaveBeenCalledWith(previous[0]?.[0], previous[0]?.[1]);
    expect(mocks.toastError).toHaveBeenCalledWith("boom");
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
