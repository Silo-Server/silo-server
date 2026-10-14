import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemDetail } from "@/api/types";

import syncProgressOk from "../../../../contracts/api/v2/fixtures/sync_progress_ok.json";

const mocks = vi.hoisted(() => ({
  useQuery: vi.fn(),
  useQueries: vi.fn(),
  useMutation: vi.fn(),
  useQueryClient: vi.fn(),
  fetchCatalogItemDetail: vi.fn(),
  v2: vi.fn(),
  removeItemFromHomeSectionCaches: vi.fn(),
  cancelItemDetailQueries: vi.fn(),
  dropInactiveWatchDetail: vi.fn(),
  updateCatalogItemDetail: vi.fn(),
  invalidateMediaSurfaceQueries: vi.fn(),
  bumpHomeRefreshSignal: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (...args: unknown[]) => mocks.useQuery(...args),
  useQueries: (...args: unknown[]) => mocks.useQueries(...args),
  useMutation: (...args: unknown[]) => mocks.useMutation(...args),
  useQueryClient: () => mocks.useQueryClient(),
}));

vi.mock("@/api/v2/request", async () => {
  const actual = await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request");
  return { ...actual, v2: (...args: unknown[]) => mocks.v2(...args) };
});

vi.mock("./catalogRead", () => ({
  fetchCatalogItemDetail: (...args: unknown[]) => mocks.fetchCatalogItemDetail(...args),
}));

vi.mock("./mediaSurfaceRefresh", () => ({
  removeItemFromHomeSectionCaches: (...args: unknown[]) =>
    mocks.removeItemFromHomeSectionCaches(...args),
  cancelItemDetailQueries: (...args: unknown[]) => mocks.cancelItemDetailQueries(...args),
  dropInactiveWatchDetail: (...args: unknown[]) => mocks.dropInactiveWatchDetail(...args),
  updateCatalogItemDetail: (...args: unknown[]) => mocks.updateCatalogItemDetail(...args),
  invalidateMediaSurfaceQueries: (...args: unknown[]) =>
    mocks.invalidateMediaSurfaceQueries(...args),
}));

vi.mock("@/pages/homeSurfaceRefresh", () => ({
  bumpHomeRefreshSignal: (...args: unknown[]) => mocks.bumpHomeRefreshSignal(...args),
}));

vi.mock("sonner", () => ({
  toast: {
    success: (...args: unknown[]) => mocks.toastSuccess(...args),
    error: (...args: unknown[]) => mocks.toastError(...args),
  },
}));

import {
  useClearResumePosition,
  useContinueWatching,
  useReportMediaProgress,
  useResetProgressAction,
} from "./progress";

describe("useContinueWatching", () => {
  const signal = new AbortController().signal;
  beforeEach(() => {
    mocks.useQuery.mockReset();
    mocks.useQueries.mockReset();
    mocks.fetchCatalogItemDetail.mockReset();
    mocks.fetchCatalogItemDetail.mockResolvedValue({
      content_id: "movie-123",
      title: "Catalog Detail",
      type: "movie",
    });
    mocks.useQuery.mockReturnValue({
      data: {
        items: [{ media_item_id: "movie-123" }],
      },
      isLoading: false,
    });
    mocks.useQueries.mockImplementation(
      ({
        queries,
      }: {
        queries: Array<{ queryFn: (context: { signal: AbortSignal }) => Promise<unknown> }>;
      }) => {
        void queries[0]?.queryFn({ signal });
        return [{ data: undefined, isLoading: false }];
      },
    );
  });

  it("looks up continue-watching details through catalog item detail", () => {
    useContinueWatching();

    expect(mocks.fetchCatalogItemDetail).toHaveBeenCalledWith("movie-123", undefined, { signal });
  });
});

describe("useReportMediaProgress", () => {
  type ReportOptions = {
    mutationFn: (vars: {
      contentId: string;
      positionSeconds: number;
      durationSeconds: number;
      forceOverwrite?: boolean;
    }) => Promise<unknown>;
    onSuccess?: (data: unknown, vars: { contentId: string }) => void;
  };

  beforeEach(() => {
    mocks.useMutation.mockReset();
    mocks.useMutation.mockImplementation((options: unknown) => options);
    mocks.useQueryClient.mockReset();
    mocks.useQueryClient.mockReturnValue({ invalidateQueries: vi.fn() });
    mocks.v2.mockReset();
    mocks.v2.mockResolvedValue(syncProgressOk);
  });

  it("sends one syncProgress item with millisecond positions and returns the results summary", async () => {
    const options = useReportMediaProgress() as unknown as ReportOptions;

    const result = await options.mutationFn({
      contentId: "movie-8f2c1a",
      positionSeconds: 1325.5,
      durationSeconds: 10200,
    });

    expect(mocks.v2).toHaveBeenCalledWith("POST /api/v2/sync/progress", {
      body: {
        items: [
          {
            media_item_id: "movie-8f2c1a",
            position_ms: 1325500,
            duration_ms: 10200000,
            force_overwrite: true,
          },
        ],
      },
    });
    expect(result).toEqual(syncProgressOk);
    expect(syncProgressOk.items[0]?.status).toBe("success");
  });

  it("rejects an item failure inside a successful HTTP batch", async () => {
    mocks.v2.mockResolvedValue({
      items: [
        {
          index: 0,
          media_item_id: "hidden",
          status: "failure",
          failure: {
            type: "https://siloserver.org/docs/api/v2/problems/not_found",
            title: "Not found",
            status: 404,
            detail: "Catalog item not found.",
          },
        },
      ],
      summary: { total: 1, succeeded: 0, failed: 1 },
    });
    const options = useReportMediaProgress() as unknown as ReportOptions;
    await expect(
      options.mutationFn({ contentId: "hidden", positionSeconds: 1, durationSeconds: 10 }),
    ).rejects.toThrow("Catalog item not found.");
  });

  it("scopes the post-sync invalidation to progress and the reported item", async () => {
    const invalidateQueries = vi.fn();
    mocks.useQueryClient.mockReturnValue({ invalidateQueries });
    const options = useReportMediaProgress() as unknown as ReportOptions;

    options.onSuccess?.(syncProgressOk, { contentId: "movie-8f2c1a" });

    expect(invalidateQueries).toHaveBeenCalledTimes(2);
    expect(invalidateQueries.mock.calls[1]?.[0]?.queryKey).toEqual(
      expect.arrayContaining(["movie-8f2c1a"]),
    );
  });
});

describe("useClearResumePosition", () => {
  type ClearOptions = {
    mutationFn: (vars: { contentId: string }) => Promise<unknown>;
    onSuccess: (data: unknown, vars: { contentId: string }) => Promise<void>;
  };

  beforeEach(() => {
    mocks.useMutation.mockReset();
    mocks.useMutation.mockImplementation((options: unknown) => options);
    mocks.useQueryClient.mockReset();
    mocks.v2.mockReset();
    mocks.v2.mockResolvedValue(undefined);
    mocks.removeItemFromHomeSectionCaches.mockReset();
    mocks.cancelItemDetailQueries.mockReset();
    mocks.cancelItemDetailQueries.mockResolvedValue(undefined);
    mocks.dropInactiveWatchDetail.mockReset();
    mocks.updateCatalogItemDetail.mockReset();
    mocks.invalidateMediaSurfaceQueries.mockReset();
    mocks.invalidateMediaSurfaceQueries.mockResolvedValue(undefined);
    mocks.bumpHomeRefreshSignal.mockReset();
    mocks.toastSuccess.mockReset();
  });

  it("clears the resume position through clearResumePosition", async () => {
    mocks.useQueryClient.mockReturnValue({});
    const options = useClearResumePosition() as unknown as ClearOptions;

    await options.mutationFn({ contentId: "movie-8f2c1a" });

    expect(mocks.v2).toHaveBeenCalledWith("DELETE /api/v2/progress/{media_item_id}/position", {
      path: { media_item_id: "movie-8f2c1a" },
    });
  });

  it("takes the item off Continue Watching and drops its cached resume point", async () => {
    const queryClient = {};
    mocks.useQueryClient.mockReturnValue(queryClient);
    const related = new Set(["movie-8f2c1a"]);
    mocks.dropInactiveWatchDetail.mockReturnValue(related);
    const options = useClearResumePosition() as unknown as ClearOptions;

    await options.onSuccess(undefined, { contentId: "movie-8f2c1a" });

    expect(mocks.cancelItemDetailQueries).toHaveBeenCalledWith(queryClient, "movie-8f2c1a");
    expect(mocks.updateCatalogItemDetail.mock.calls[0]?.[1]).toBe("movie-8f2c1a");
    expect(mocks.removeItemFromHomeSectionCaches).toHaveBeenCalledWith(
      queryClient,
      "movie-8f2c1a",
      "continue_watching",
    );
    // A closed player's cached watch detail is dropped, and the series it led
    // to still refreshes.
    expect(mocks.dropInactiveWatchDetail).toHaveBeenCalledWith(queryClient, "movie-8f2c1a");
    expect(mocks.invalidateMediaSurfaceQueries).toHaveBeenCalledWith(queryClient, {
      itemId: "movie-8f2c1a",
      skipSimilarItems: true,
      knownRelatedIds: related,
    });
    // Once at once from the patched cache, once after the server refresh.
    expect(mocks.bumpHomeRefreshSignal).toHaveBeenCalledTimes(2);
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Progress reset");

    const updater = mocks.updateCatalogItemDetail.mock.calls[0]?.[2] as (
      detail: ItemDetail,
    ) => ItemDetail;
    // The API sends the season counters on a movie's detail too.
    const cleared = updater({
      content_id: "movie-8f2c1a",
      type: "movie",
      user_data: {
        played: true,
        is_in_progress: true,
        position_seconds: 1325.5,
        duration_seconds: 5400,
        watched_count: 0,
        unplayed_count: 0,
        in_progress_count: 0,
      },
    } as unknown as ItemDetail);
    expect(cleared.user_data).toEqual({
      played: true,
      is_in_progress: false,
      position_seconds: 0,
      duration_seconds: 5400,
      watched_count: 0,
      unplayed_count: 0,
      in_progress_count: 0,
    });
  });
});

describe("useResetProgressAction", () => {
  const mutate = vi.fn();

  beforeEach(() => {
    mutate.mockReset();
    mocks.useMutation.mockReset();
    mocks.useMutation.mockReturnValue({ mutate });
    mocks.useQueryClient.mockReset();
    mocks.useQueryClient.mockReturnValue({});
  });

  it("offers the reset for a movie or episode with a resume position", () => {
    for (const type of ["movie", "episode"] as const) {
      // As the API answers a leaf: the season counters come along too.
      // A rewatch: watched and with a live resume point.
      const userData = {
        played: true,
        position_seconds: 120,
        is_in_progress: true,
        watched_count: 0,
        unplayed_count: 0,
        in_progress_count: 0,
      } as unknown as ItemDetail["user_data"];
      const { result } = renderHook(() =>
        useResetProgressAction({ content_id: `${type}-1`, type, user_data: userData }),
      );

      result.current?.();
      expect(mutate).toHaveBeenLastCalledWith({ contentId: `${type}-1` });
    }
  });

  it("offers nothing without a resume position or for other kinds", () => {
    const items: Array<Pick<ItemDetail, "content_id" | "type" | "user_data">> = [
      { content_id: "movie-1", type: "movie", user_data: { played: true, position_seconds: 0 } },
      { content_id: "episode-1", type: "episode" },
      {
        content_id: "season-1",
        type: "season",
        user_data: { played: false, watched_count: 1, unplayed_count: 2, in_progress_count: 1 },
      },
      {
        content_id: "audiobook-1",
        type: "audiobook",
        user_data: { played: false, position_seconds: 300 },
      },
    ];
    for (const item of items) {
      const { result } = renderHook(() => useResetProgressAction(item));
      expect(result.current, item.content_id).toBeUndefined();
    }
  });
});
