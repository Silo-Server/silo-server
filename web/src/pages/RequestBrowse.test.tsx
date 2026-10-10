import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import type { DiscoverBrowseResponse } from "@/api/types";

const mocks = vi.hoisted(() => ({
  useRequestBrowse: vi.fn(),
  fetchNextPage: vi.fn(),
  mutate: vi.fn(),
}));

vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestBrowse: (...args: unknown[]) => mocks.useRequestBrowse(...args),
  useCreateMediaRequest: () => ({ mutateAsync: mocks.mutate, isPending: false }),
}));
vi.mock("@/hooks/useDocumentTitle", () => ({ useDocumentTitle: () => {} }));
vi.mock("@/hooks/useWatchlistTitleToggle", () => ({
  useWatchlistTitleToggle: () => ({ enabled: false, toggle: vi.fn(), isPending: () => false }),
}));

import RequestBrowse from "./RequestBrowse";

function browse(page: number): DiscoverBrowseResponse {
  return {
    kind: "genre",
    slug: "drama",
    display_name: "Drama",
    media_type: "movie",
    sort: "popularity",
    page,
    total_pages: 4,
    results: [
      {
        media_type: "movie",
        tmdb_id: 42,
        title: "Heat",
        availability: "missing",
        request: { requestable: true },
      },
    ],
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
        <Route path="/requests/browse/genre/:slug" element={<RequestBrowse kind="genre" />} />
        <Route path="*" element={null} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
  );
}

function loaded(pages: DiscoverBrowseResponse[], overrides: Record<string, unknown> = {}) {
  return {
    data: { pages, pageParams: pages.map((page) => page.page) },
    isLoading: false,
    isError: false,
    isFetchNextPageError: false,
    hasNextPage: true,
    isFetchingNextPage: false,
    fetchNextPage: mocks.fetchNextPage,
    ...overrides,
  };
}

describe("RequestBrowse", () => {
  beforeEach(() => {
    mocks.mutate.mockReset();
    mocks.fetchNextPage.mockReset();
    mocks.useRequestBrowse.mockReset();
    mocks.useRequestBrowse.mockReturnValue(loaded([browse(1)]));
  });

  it("shows every loaded page and loads the next from the foot of the grid", () => {
    const second = browse(2);
    second.results = [
      {
        media_type: "movie",
        tmdb_id: 43,
        title: "Ronin",
        availability: "missing",
        request: { requestable: true },
      },
    ];
    mocks.useRequestBrowse.mockReturnValue(loaded([browse(1), second]));
    renderAt("/requests/browse/genre/drama?media_type=movie");

    expect(screen.getAllByRole("link", { name: "Heat" }).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("link", { name: "Ronin" }).length).toBeGreaterThan(0);

    // jsdom has no IntersectionObserver, so the foot offers a button.
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(mocks.fetchNextPage).toHaveBeenCalledTimes(1);
  });

  it("stops offering more at the end of the list", () => {
    mocks.useRequestBrowse.mockReturnValue(loaded([browse(1)], { hasNextPage: false }));
    renderAt("/requests/browse/genre/drama?media_type=movie");

    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("keeps loading past a page a rating limit emptied", () => {
    const empty = { ...browse(1), results: [] };
    mocks.useRequestBrowse.mockReturnValue(loaded([empty]));
    renderAt("/requests/browse/genre/drama?media_type=movie");

    expect(screen.queryByText(/Nothing matched/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    expect(mocks.fetchNextPage).toHaveBeenCalledTimes(1);

    cleanup();
    mocks.useRequestBrowse.mockReturnValue(loaded([empty], { hasNextPage: false }));
    renderAt("/requests/browse/genre/drama?media_type=movie");
    expect(screen.getByText("Nothing matched. Try a different sort.")).toBeInTheDocument();
  });

  it("drops a legacy page parameter when the media type changes", () => {
    renderAt("/requests/browse/genre/drama?media_type=movie&page=3");

    fireEvent.mouseDown(screen.getByRole("tab", { name: "Series" }));

    expect(screen.getByTestId("location")).toHaveTextContent(
      "/requests/browse/genre/drama?media_type=series",
    );
    expect(screen.getByTestId("location")).not.toHaveTextContent("page=");
  });

  it("requests a title from its card", () => {
    mocks.mutate.mockResolvedValue({});
    renderAt("/requests/browse/genre/drama");

    fireEvent.click(screen.getByRole("button", { name: /^Request Heat/ }));

    expect(mocks.mutate).toHaveBeenCalledWith(
      expect.objectContaining({ media_type: "movie", tmdb_id: 42 }),
    );
  });
});
