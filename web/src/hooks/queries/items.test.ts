import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  v2: vi.fn(),
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

vi.mock("@/api/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/client")>()),
  api: mocks.api,
}));

vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: (...args: unknown[]) => mocks.v2(...args) };
});

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

import { useRefreshItemMetadata, useWatchedStateMutation } from "./items";

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

afterEach(() => vi.unstubAllGlobals());

describe("item query helpers", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.api.mockResolvedValue({});
    mocks.v2.mockReset();
    mocks.v2.mockResolvedValue({
      content_id: "ebook 1/isbn:978",
      type: "ebook",
      title: "Book",
      versions: [],
      subtitles: [],
    });
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

  it("optimistically flips played state and reverts only that field on failure", async () => {
    const queryClient = {};
    mocks.useQueryClient.mockReturnValue(queryClient);
    let finishCancellation!: () => void;
    mocks.cancelItemDetailQueries.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        finishCancellation = resolve;
      }),
    );
    useWatchedStateMutation({ content_id: "series-1", type: "series" });
    const options = mocks.useMutation.mock.calls[
      mocks.useMutation.mock.calls.length - 1
    ]?.[0] as WatchedMutationOptions;

    const updating = options.onMutate?.(true);
    expect(mocks.cancelItemDetailQueries).toHaveBeenCalledWith(queryClient, "series-1");
    expect(mocks.updateCatalogItemDetail).not.toHaveBeenCalled();
    finishCancellation();
    await updating;
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
        user_state: { played: true, is_favorite: true, in_watchlist: true },
      }),
    ).toMatchObject({
      user_data: { played: false },
      user_state: { played: false, is_favorite: true, in_watchlist: true },
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
});
