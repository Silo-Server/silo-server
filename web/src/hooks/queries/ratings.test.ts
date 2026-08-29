import { beforeEach, describe, expect, it, vi } from "vitest";
import { catalogKeys, itemKeys, ratingKeys } from "./keys";

const mocks = vi.hoisted(() => ({
  cancelItemDetailQueries: vi.fn(),
  cancelQueries: vi.fn(),
  getQueriesData: vi.fn(),
  getQueryData: vi.fn(),
  setQueryData: vi.fn(),
  invalidateQueries: vi.fn(),
  useMutation: vi.fn(),
  useQuery: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useMutation: (...args: unknown[]) => mocks.useMutation(...args),
    useQuery: (...args: unknown[]) => mocks.useQuery(...args),
    useQueryClient: () => ({
      cancelQueries: mocks.cancelQueries,
      getQueriesData: mocks.getQueriesData,
      getQueryData: mocks.getQueryData,
      setQueryData: mocks.setQueryData,
      invalidateQueries: mocks.invalidateQueries,
    }),
  };
});

vi.mock("./mediaSurfaceRefresh", async () => {
  const actual =
    await vi.importActual<typeof import("./mediaSurfaceRefresh")>("./mediaSurfaceRefresh");
  return {
    ...actual,
    cancelItemDetailQueries: (...args: unknown[]) => mocks.cancelItemDetailQueries(...args),
    updateCatalogItemDetail: vi.fn(),
  };
});

vi.mock("./ratingsSurfaceRefresh", () => ({
  invalidateRatingSurfaceQueries: vi.fn(),
}));

import {
  useCommunityRatings,
  useDeleteRating,
  useSetCommunityRatingReaction,
  useSetRating,
} from "./ratings";

describe("rating mutations", () => {
  beforeEach(() => {
    mocks.cancelItemDetailQueries.mockReset();
    mocks.cancelItemDetailQueries.mockResolvedValue(undefined);
    mocks.cancelQueries.mockReset();
    mocks.cancelQueries.mockResolvedValue(undefined);
    mocks.getQueriesData.mockReset();
    mocks.getQueriesData.mockReturnValue([]);
    mocks.getQueryData.mockReset();
    mocks.setQueryData.mockReset();
    mocks.invalidateQueries.mockReset();
    mocks.useMutation.mockReset();
    mocks.useMutation.mockImplementation((options: unknown) => options);
    mocks.useQuery.mockReset();
    mocks.useQuery.mockImplementation((options: unknown) => options);
  });

  it("loads community ratings only after capability discovery enables them", () => {
    useCommunityRatings("item-1");
    expect(mocks.useQuery.mock.calls[0]?.[0]).toMatchObject({
      queryKey: ratingKeys.capabilities(),
      staleTime: Infinity,
      gcTime: Infinity,
    });
    expect(mocks.useQuery.mock.calls[1]?.[0]).toMatchObject({
      queryKey: ratingKeys.community("item-1"),
      enabled: false,
    });

    mocks.useQuery.mockReset();
    mocks.useQuery
      .mockImplementationOnce((options: unknown) => ({
        ...(options as object),
        data: { community_ratings: true, community_rating_reactions: true },
        isError: false,
        isLoading: false,
      }))
      .mockImplementationOnce((options: unknown) => options);

    useCommunityRatings("item-1");
    expect(mocks.useQuery.mock.calls[1]?.[0]).toMatchObject({
      queryKey: ratingKeys.community("item-1"),
      enabled: true,
    });
  });

  it.each([
    ["setting", () => useSetRating("item-1"), 4],
    ["deleting", () => useDeleteRating("item-1"), undefined],
  ])(
    "snapshots both item-detail cache shapes when %s a rating",
    async (_label, useRating, value) => {
      useRating();
      const options = mocks.useMutation.mock.calls[0]?.[0] as {
        scope: { id: string };
        onMutate: (rating?: number) => Promise<unknown>;
      };

      await options.onMutate(value);

      expect(options.scope).toEqual({ id: "rating:item-1" });
      const filters = mocks.getQueriesData.mock.calls[0]?.[0];
      expect(filters.predicate({ queryKey: catalogKeys.itemDetail("item-1") })).toBe(true);
      expect(filters.predicate({ queryKey: itemKeys.detail("item-1") })).toBe(true);
      expect(filters.predicate({ queryKey: catalogKeys.itemDetail("item-2") })).toBe(false);
    },
  );

  it("optimistically edits the viewer's existing card without changing its identity", async () => {
    let cached = {
      average_rating: 4.5,
      vote_count: 2,
      ratings: [
        {
          key: "stable-viewer-card",
          display_name: "b***",
          rating: 5,
          rated_at: "2026-08-13T08:00:00Z",
          up_count: 1,
          down_count: 0,
          is_viewer: true,
        },
        {
          key: "other-card",
          display_name: "b***",
          rating: 4,
          rated_at: "2026-08-12T08:00:00Z",
          up_count: 0,
          down_count: 0,
          is_viewer: false,
        },
      ],
    };
    mocks.getQueryData.mockReturnValue(cached);
    mocks.setQueryData.mockImplementation((_key: unknown, update: unknown) => {
      if (typeof update === "function") {
        cached = update(cached);
      }
    });

    useSetRating("item-1");
    const options = mocks.useMutation.mock.calls[0]?.[0] as {
      onMutate: (rating: number) => Promise<unknown>;
    };

    await options.onMutate(2);

    expect(mocks.cancelQueries).toHaveBeenCalledWith({
      queryKey: ratingKeys.community("item-1"),
    });
    expect(cached.average_rating).toBe(3);
    expect(cached.vote_count).toBe(2);
    expect(cached.ratings[0]).toMatchObject({
      key: "stable-viewer-card",
      rating: 2,
      is_viewer: true,
    });
    expect(cached.ratings[1]).toMatchObject({ key: "other-card", rating: 4 });
  });

  it("optimistically removes only the viewer's card when their rating is cleared", async () => {
    let cached = {
      average_rating: 4.5,
      vote_count: 2,
      ratings: [
        {
          key: "stable-viewer-card",
          display_name: "b***",
          rating: 5,
          rated_at: "2026-08-13T08:00:00Z",
          up_count: 1,
          down_count: 0,
          is_viewer: true,
        },
        {
          key: "other-card",
          display_name: "b***",
          rating: 4,
          rated_at: "2026-08-12T08:00:00Z",
          up_count: 0,
          down_count: 0,
          is_viewer: false,
        },
      ],
    };
    mocks.getQueryData.mockReturnValue(cached);
    mocks.setQueryData.mockImplementation((_key: unknown, update: unknown) => {
      if (typeof update === "function") {
        cached = update(cached);
      }
    });

    useDeleteRating("item-1");
    const options = mocks.useMutation.mock.calls[0]?.[0] as {
      onMutate: () => Promise<unknown>;
    };

    await options.onMutate();

    expect(cached).toMatchObject({
      average_rating: 4,
      vote_count: 1,
      ratings: [{ key: "other-card", rating: 4, is_viewer: false }],
    });
  });

  it("optimistically moves a community reaction between separate tallies", async () => {
    let cached = {
      average_rating: 4,
      vote_count: 1,
      ratings: [
        {
          key: "rating-1",
          display_name: "Sam***",
          rating: 4,
          up_count: 2,
          down_count: 5,
          viewer_reaction: "up" as const,
          is_viewer: false,
        },
      ],
    };
    mocks.getQueryData.mockReturnValue(cached);
    mocks.setQueryData.mockImplementation((_key: unknown, update: unknown) => {
      if (typeof update === "function") {
        cached = update(cached);
      }
    });

    useSetCommunityRatingReaction("item-1");
    const options = mocks.useMutation.mock.calls[0]?.[0] as {
      onMutate: (variables: {
        ratingKey: string;
        reaction: "up" | "down" | null;
      }) => Promise<unknown>;
    };

    await options.onMutate({ ratingKey: "rating-1", reaction: "down" });

    expect(cached.ratings[0]).toMatchObject({
      up_count: 1,
      down_count: 6,
      viewer_reaction: "down",
    });
  });

  it("optimistically removes the viewer's selected reaction from their own card", async () => {
    let cached = {
      average_rating: 5,
      vote_count: 1,
      ratings: [
        {
          key: "rating-viewer",
          display_name: "B***",
          rated_at: "2026-08-29T08:00:00Z",
          rating: 5,
          up_count: 1,
          down_count: 0,
          viewer_reaction: "up" as const,
          is_viewer: true,
        },
      ],
    };
    mocks.getQueryData.mockReturnValue(cached);
    mocks.setQueryData.mockImplementation((_key: unknown, update: unknown) => {
      if (typeof update === "function") {
        cached = update(cached);
      }
    });

    useSetCommunityRatingReaction("item-1");
    const options = mocks.useMutation.mock.calls[0]?.[0] as {
      onMutate: (variables: {
        ratingKey: string;
        reaction: "up" | "down" | null;
      }) => Promise<unknown>;
    };

    await options.onMutate({ ratingKey: "rating-viewer", reaction: null });

    expect(cached.ratings[0]).toMatchObject({
      up_count: 0,
      down_count: 0,
      viewer_reaction: undefined,
      is_viewer: true,
    });
  });
});
