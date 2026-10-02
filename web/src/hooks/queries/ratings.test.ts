import { beforeEach, describe, expect, it, vi } from "vitest";
import { catalogKeys, itemKeys } from "./keys";

const mocks = vi.hoisted(() => ({
  cancelItemDetailQueries: vi.fn(),
  getQueriesData: vi.fn(),
  useMutation: vi.fn(),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual =
    await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useMutation: (...args: unknown[]) => mocks.useMutation(...args),
    useQueryClient: () => ({ getQueriesData: mocks.getQueriesData }),
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

import { useDeleteRating, useSetRating } from "./ratings";

describe("rating mutations", () => {
  beforeEach(() => {
    mocks.cancelItemDetailQueries.mockReset();
    mocks.cancelItemDetailQueries.mockResolvedValue(undefined);
    mocks.getQueriesData.mockReset();
    mocks.getQueriesData.mockReturnValue([]);
    mocks.useMutation.mockReset();
    mocks.useMutation.mockImplementation((options: unknown) => options);
  });

  it.each([
    ["setting", () => useSetRating("item-1"), 4],
    ["deleting", () => useDeleteRating("item-1"), undefined],
  ])(
    "snapshots both item-detail cache shapes when %s a rating",
    async (_label, useRating, value) => {
      useRating();
      const options = mocks.useMutation.mock.calls[0]?.[0] as {
        onMutate: (rating?: number) => Promise<unknown>;
      };

      await options.onMutate(value);

      const filters = mocks.getQueriesData.mock.calls[0]?.[0];
      expect(filters.predicate({ queryKey: catalogKeys.itemDetail("item-1") })).toBe(true);
      expect(filters.predicate({ queryKey: itemKeys.detail("item-1") })).toBe(true);
      expect(filters.predicate({ queryKey: catalogKeys.itemDetail("item-2") })).toBe(false);
    },
  );
});
