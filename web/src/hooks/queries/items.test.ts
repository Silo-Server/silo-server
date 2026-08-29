import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  cancelItemDetailQueries: vi.fn(),
  invalidateMediaSurfaceQueries: vi.fn(),
  scheduleMediaSurfaceInvalidation: vi.fn(),
  toastError: vi.fn(),
  toastLoading: vi.fn(),
  toastSuccess: vi.fn(),
  toastWarning: vi.fn(),
  updateCatalogItemDetail: vi.fn(),
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
  cancelItemDetailQueries: (...args: unknown[]) => mocks.cancelItemDetailQueries(...args),
  invalidateMediaSurfaceQueries: (...args: unknown[]) =>
    mocks.invalidateMediaSurfaceQueries(...args),
  scheduleMediaSurfaceInvalidation: (...args: unknown[]) =>
    mocks.scheduleMediaSurfaceInvalidation(...args),
  updateCatalogItemDetail: (...args: unknown[]) => mocks.updateCatalogItemDetail(...args),
}));

vi.mock("@/pages/homeSurfaceRefresh", () => ({
  bumpHomeRefreshSignal: vi.fn(),
}));

vi.mock("sonner", () => ({
  toast: {
    error: (...args: unknown[]) => mocks.toastError(...args),
    loading: (...args: unknown[]) => mocks.toastLoading(...args),
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
    warning: (...args: unknown[]) => mocks.toastWarning(...args),
  },
}));

import {
  fetchWatchDetail,
  redetectEpisodeIntro,
  useRefreshItemMetadata,
  useWatchedStateMutation,
} from "./items";

type WatchedMutationOptions = {
  mutationFn: (nextPlayed: boolean) => Promise<unknown>;
  onMutate?: (nextPlayed: boolean) => Promise<unknown>;
  onError?: (error: unknown, nextPlayed: boolean) => void;
  onSuccess?: (data: unknown, nextPlayed: boolean) => void;
  onSettled?: () => Promise<unknown>;
};

type RefreshMetadataVariables = {
  item: { content_id: string; type: string };
  mode: "quick" | "complete";
};

type RefreshMetadataContext = { toastID: string | number };

type RefreshMetadataMutationOptions = {
  onMutate?: (variables: RefreshMetadataVariables) => RefreshMetadataContext;
  onSuccess?: (
    data: { job: { result_payload?: Record<string, unknown> } },
    variables: RefreshMetadataVariables,
    context?: RefreshMetadataContext,
  ) => Promise<void>;
  onError?: (
    err: unknown,
    variables: RefreshMetadataVariables,
    context?: RefreshMetadataContext,
  ) => void;
};

describe("item query helpers", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.api.mockResolvedValue({});
    mocks.cancelItemDetailQueries.mockReset();
    mocks.cancelItemDetailQueries.mockResolvedValue(undefined);
    mocks.invalidateMediaSurfaceQueries.mockReset();
    mocks.scheduleMediaSurfaceInvalidation.mockReset();
    mocks.toastError.mockReset();
    mocks.toastLoading.mockReset();
    mocks.toastLoading.mockReturnValue("refresh-toast");
    mocks.toastSuccess.mockReset();
    mocks.toastWarning.mockReset();
    mocks.updateCatalogItemDetail.mockReset();
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

  it("shows one spinning refresh notification and replaces it with success", async () => {
    const invalidateQueries = vi.fn().mockResolvedValue(undefined);
    mocks.useQueryClient.mockReturnValue({ invalidateQueries });

    useRefreshItemMetadata();
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as RefreshMetadataMutationOptions;
    const variables: RefreshMetadataVariables = {
      item: { content_id: "series-1", type: "series" },
      mode: "quick",
    };

    const context = options.onMutate?.(variables);
    expect(mocks.toastLoading).toHaveBeenCalledWith("Quick metadata refresh running…");

    await options.onSuccess?.({ job: { result_payload: {} } }, variables, context);
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Metadata refreshed", {
      id: "refresh-toast",
    });
  });

  it("warns when the refresh succeeded but artwork caching did not finish", async () => {
    const invalidateQueries = vi.fn().mockResolvedValue(undefined);
    mocks.useQueryClient.mockReturnValue({ invalidateQueries });

    useRefreshItemMetadata();
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as RefreshMetadataMutationOptions;
    const variables: RefreshMetadataVariables = {
      item: { content_id: "series-1", type: "series" },
      mode: "quick",
    };

    const context = options.onMutate?.(variables);
    await options.onSuccess?.(
      {
        job: {
          result_payload: {
            refresh_content_id: "series-1",
            artwork_cache_warning: "2 refreshed artwork image(s) failed to cache",
          },
        },
      },
      variables,
      context,
    );

    expect(mocks.toastSuccess).not.toHaveBeenCalled();
    expect(mocks.toastWarning).toHaveBeenCalledWith(
      "Metadata refreshed, but artwork caching did not finish",
      {
        id: "refresh-toast",
        description: "2 refreshed artwork image(s) failed to cache",
      },
    );
    // The refresh still committed, so the caches must be invalidated anyway.
    expect(invalidateQueries).toHaveBeenCalled();
  });

  it("replaces the spinning refresh notification with a failure", () => {
    useRefreshItemMetadata();
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as RefreshMetadataMutationOptions;
    const variables: RefreshMetadataVariables = {
      item: { content_id: "series-1", type: "series" },
      mode: "quick",
    };
    const context = options.onMutate?.(variables);

    options.onError?.(new Error("Artwork download failed"), variables, context);
    expect(mocks.toastError).toHaveBeenCalledWith("Artwork download failed", {
      id: "refresh-toast",
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

  it("optimistically flips played state and reverts only that field on failure", async () => {
    useWatchedStateMutation({ content_id: "series-1", type: "series" });
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as WatchedMutationOptions;

    await options.onMutate?.(true);
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

    // Reverting restores only this mutation's own field, so a concurrent
    // favorite/watchlist toggle's optimistic state survives the failure.
    options.onError?.(new Error("boom"), true);
    const revert = mocks.updateCatalogItemDetail.mock.calls[1]?.[2] as (
      detail: Record<string, unknown>,
    ) => Record<string, unknown>;
    expect(
      revert({
        user_data: { played: true },
        user_state: { played: true, is_favorite: true, in_watchlist: false },
      }),
    ).toMatchObject({
      user_data: { played: false },
      user_state: { played: false, is_favorite: true, in_watchlist: false },
    });
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

    options.onSettled?.();
    // The item's own detail is deliberately not skipped: the server also zeroes
    // the resume position, which the optimistic patch cannot reconstruct.
    expect(mocks.scheduleMediaSurfaceInvalidation).toHaveBeenCalledWith(expect.anything(), {
      itemId: "ebook-1",
      watchedKeys: [],
      skipSimilarItems: true,
    });
  });

  it("updates watched state optimistically before refreshing derived surfaces", async () => {
    const queryClient = { getQueriesData: vi.fn(() => []) };
    mocks.useQueryClient.mockReturnValue(queryClient);

    useWatchedStateMutation({ content_id: "movie-1", type: "movie" });
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as WatchedMutationOptions;

    await options.onMutate?.(true);

    expect(mocks.cancelItemDetailQueries).toHaveBeenCalledWith(queryClient, "movie-1");
    expect(mocks.updateCatalogItemDetail).toHaveBeenCalledWith(
      queryClient,
      "movie-1",
      expect.any(Function),
    );
    const updater = mocks.updateCatalogItemDetail.mock.calls[0]?.[2] as (
      detail: ItemDetail,
    ) => ItemDetail;
    expect(
      updater({ user_data: { played: false }, user_state: { played: false } } as ItemDetail),
    ).toMatchObject({
      user_data: { played: true },
      user_state: { played: true },
    });

    options.onError?.(new Error("failed"), true);
    const rollback = mocks.updateCatalogItemDetail.mock.calls[1]?.[2] as (
      detail: ItemDetail,
    ) => ItemDetail;
    expect(
      rollback({
        user_data: { played: true },
        user_state: { played: true, is_favorite: true, in_watchlist: true },
      } as ItemDetail),
    ).toMatchObject({
      user_data: { played: false },
      user_state: { played: false, is_favorite: true, in_watchlist: true },
    });
  });
});
