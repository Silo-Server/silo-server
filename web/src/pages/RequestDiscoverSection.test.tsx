import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import type { RequestDiscoverySection } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";

const mocks = vi.hoisted(() => ({
  useRequestDiscoverySection: vi.fn(),
  fetchNextPage: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestDiscoverySection: (...args: unknown[]) => mocks.useRequestDiscoverySection(...args),
  useCreateMediaRequest: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));

import RequestDiscoverSection from "./RequestDiscoverSection";

function section(page: number, tmdbIDs: number[]): RequestDiscoverySection {
  return {
    key: "trending_movies",
    title: "Trending Movies",
    page,
    total_pages: 3,
    total_results: 60,
    results: tmdbIDs.map((tmdbID) => ({
      media_type: "movie",
      tmdb_id: tmdbID,
      title: `Movie ${tmdbID}`,
      availability: "missing",
      request: { requestable: true },
    })),
  };
}

function loaded(pages: RequestDiscoverySection[], overrides: Record<string, unknown> = {}) {
  return {
    data: { pages, pageParams: pages.map((page) => page.page) },
    isLoading: false,
    isError: false,
    isFetchNextPageError: false,
    hasNextPage: true,
    isFetchingNextPage: false,
    fetchNextPage: mocks.fetchNextPage,
    refetch: vi.fn(),
    ...overrides,
  };
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>;
}

function renderAt(url: string) {
  render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/requests/discover/:section" element={<RequestDiscoverSection />} />
        <Route path="*" element={null} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
  );
}

describe("RequestDiscoverSection", () => {
  beforeEach(() => {
    mocks.useRequestDiscoverySection.mockReset();
    mocks.fetchNextPage.mockReset();
    mocks.useRequestDiscoverySection.mockReturnValue(loaded([section(1, [100])]));
  });

  it("shows every loaded page of a Discover row as one grid, without a pager", () => {
    // TMDB's list shifted between reads, so page 2 repeats a page-1 title.
    mocks.useRequestDiscoverySection.mockReturnValue(
      loaded([section(1, [100, 101]), section(2, [101, 200])]),
    );
    renderAt("/requests/discover/trending_movies");

    expect(mocks.useRequestDiscoverySection).toHaveBeenLastCalledWith("trending_movies");
    expect(screen.getByRole("heading", { level: 1, name: "Trending Movies" })).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: "Movie 200" })[0]).toHaveAttribute(
      "href",
      "/title/movie/200",
    );
    expect(screen.getAllByRole("link", { name: "Movie 101" })).toHaveLength(
      screen.getAllByRole("link", { name: "Movie 100" }).length,
    );
    expect(screen.queryByRole("navigation", { name: "Result pages" })).not.toBeInTheDocument();
    expect(screen.queryByText(/Page \d/)).not.toBeInTheDocument();
  });

  it("loads the next page from the foot of the grid", () => {
    renderAt("/requests/discover/trending_movies");

    // jsdom has no IntersectionObserver, so the foot offers a button.
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));

    expect(mocks.fetchNextPage).toHaveBeenCalledTimes(1);
  });

  it("continues the grid with placeholders while the next page loads", () => {
    mocks.useRequestDiscoverySection.mockReturnValue(
      loaded([section(1, [100, 101, 102])], { isFetchingNextPage: true }),
    );
    renderAt("/requests/discover/trending_movies");

    // One grid: the placeholders follow the last poster instead of starting
    // a grid (and a row) of their own.
    const grid = screen.getAllByRole("link", { name: "Movie 102" })[0]?.closest(".grid");
    expect(grid).not.toBeNull();
    const placeholders = [...(grid?.children ?? [])].filter(
      (child) => child.getAttribute("aria-hidden") === "true",
    );
    expect(placeholders).toHaveLength(3);
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("stops offering more at the end of the row", () => {
    mocks.useRequestDiscoverySection.mockReturnValue(
      loaded([section(1, [100])], { hasNextPage: false }),
    );
    renderAt("/requests/discover/trending_movies");

    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("keeps the loaded titles and offers a retry when a later page fails", () => {
    mocks.useRequestDiscoverySection.mockReturnValue(
      loaded([section(1, [100])], { isError: true, isFetchNextPageError: true }),
    );
    renderAt("/requests/discover/trending_movies");

    expect(screen.getAllByRole("link", { name: "Movie 100" }).length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(mocks.fetchNextPage).toHaveBeenCalledTimes(1);
  });

  it("keeps loading past pages a rating limit emptied", () => {
    // The server read its whole budget for a restricted profile and found
    // nothing allowed, but the row goes on.
    mocks.useRequestDiscoverySection.mockReturnValue(loaded([section(1, [])]));
    renderAt("/requests/discover/trending_movies");

    expect(screen.queryByText("Nothing here right now.")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(mocks.fetchNextPage).toHaveBeenCalledTimes(1);
  });

  it("offers a retry when a later page fails after only empty pages", () => {
    mocks.useRequestDiscoverySection.mockReturnValue(
      loaded([section(1, [])], { isError: true, isFetchNextPageError: true }),
    );
    renderAt("/requests/discover/trending_movies");

    expect(screen.queryByText(/TMDB couldn/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(mocks.fetchNextPage).toHaveBeenCalledTimes(1);
  });

  it("says the row is empty once no page is left", () => {
    mocks.useRequestDiscoverySection.mockReturnValue(
      loaded([section(1, [])], { hasNextPage: false }),
    );
    renderAt("/requests/discover/trending_movies");

    expect(screen.getByText("Nothing here right now.")).toBeInTheDocument();
  });

  it("goes back to the Requests hub", () => {
    renderAt("/requests/discover/trending_movies");

    fireEvent.click(screen.getByRole("button", { name: "Go back" }));

    expect(screen.getByTestId("location")).toHaveTextContent(/^\/requests$/);
  });

  it("says so when the row does not exist", () => {
    mocks.useRequestDiscoverySection.mockReturnValue({
      ...loaded([]),
      data: undefined,
      isError: true,
      hasNextPage: false,
      error: new V2ProblemError("getDiscoverSection", {
        type: "validation_failed",
        title: "Invalid",
        status: 422,
      } as ConstructorParameters<typeof V2ProblemError>[1]),
    });
    renderAt("/requests/discover/nope");

    expect(screen.getByRole("heading", { level: 1, name: "Not found" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });
});
