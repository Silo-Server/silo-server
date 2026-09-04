import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  useQuery: vi.fn(),
  useQueries: vi.fn(),
  fetchCatalogItemDetail: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (...args: unknown[]) => mocks.useQuery(...args),
  useQueries: (...args: unknown[]) => mocks.useQueries(...args),
}));

vi.mock("./catalogRead", () => ({
  fetchCatalogItemDetail: (...args: unknown[]) => mocks.fetchCatalogItemDetail(...args),
}));

import { useContinueWatching } from "./progress";

describe("useContinueWatching", () => {
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
      ({ queries }: { queries: Array<{ queryFn: () => Promise<unknown> }> }) => {
        void queries[0]?.queryFn();
        return [{ data: undefined, isLoading: false }];
      },
    );
  });

  it("looks up continue-watching details through catalog item detail", () => {
    useContinueWatching();

    expect(mocks.fetchCatalogItemDetail).toHaveBeenCalledWith("movie-123");
  });
});
