import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { beforeEach, describe, expect, it, vi } from "vitest";

import Catalog from "./Catalog";

const mockUseCatalogWindow = vi.fn();
const mockUsePersonSearch = vi.fn();
const mockUseRequestSearch = vi.fn();
const mockUseCanRequest = vi.fn();

// Embla measures layout jsdom does not have.
vi.mock("@/hooks/useCarouselEmbla", () => ({
  useCarouselEmbla: () => ({
    emblaRef: () => {},
    canScrollPrev: false,
    canScrollNext: false,
    scrollPrev: () => {},
    scrollNext: () => {},
  }),
}));
vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: (...args: unknown[]) => mockUseCatalogWindow(...args),
}));
vi.mock("@/hooks/queries/personSearch", () => ({
  usePersonSearch: (...args: unknown[]) => mockUsePersonSearch(...args),
}));
vi.mock("@/hooks/queries/useRequests", () => ({
  useRequestSearch: (...args: unknown[]) => mockUseRequestSearch(...args),
  useRequestFeatureStatus: () => ({ data: undefined }),
}));
vi.mock("@/hooks/useCanRequest", () => ({
  useCanRequest: () => mockUseCanRequest(),
}));
vi.mock("@/hooks/queries/boxSets", () => ({
  useBoxSetSearch: () => ({ data: [] }),
}));
vi.mock("@/hooks/useSearchMediaScope", () => ({
  useSearchMediaScope: () => ({ scope: "video", setScope: vi.fn() }),
  useSearchScopeChips: () => ({ activeScope: "video", onScopeChange: vi.fn() }),
}));
vi.mock("@/hooks/useWatchlistTitleToggle", () => ({
  useWatchlistTitleToggle: () => ({ enabled: false, isPending: () => false, toggle: vi.fn() }),
}));
vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: null, quickActionMode: "none" }),
}));
vi.mock("@/hooks/useUICustomization", () => ({
  useUICustomization: () => ({ cardPresentation: { poster_size: "default" } }),
}));
vi.mock("@/components/SearchBar", () => ({ default: () => <div /> }));
vi.mock("@/components/catalog/CatalogFiltersPanel", () => ({ default: () => <div /> }));
vi.mock("@/components/ItemCard", () => ({
  default: ({ item }: { item: { title: string } }) => <span>{item.title}</span>,
}));
vi.mock("@/components/ItemGrid", () => ({ default: () => <div data-testid="item-grid" /> }));
vi.mock("@/components/RequestPosterCard", () => ({
  default: ({ item }: { item: { title: string } }) => <span>{item.title}</span>,
}));
vi.mock("@/components/RequestToAddSection", () => ({
  RequestToAddSection: (props: { libraryHadHits: boolean; libraryResultsKnown?: boolean }) => (
    <div
      data-testid="request-grid"
      data-library-had-hits={String(props.libraryHadHits)}
      data-library-results-known={String(props.libraryResultsKnown)}
    />
  ),
}));

const titles = (count: number) =>
  Array.from({ length: count }, (_, i) => ({
    content_id: `m${i}`,
    title: `Film ${i}`,
    type: "movie",
  }));

function libraryResults(count: number, extra: Record<string, unknown> = {}) {
  mockUseCatalogWindow.mockReturnValue({
    data: {
      totalItems: count,
      knownItems: count,
      pages: new Map([[0, titles(count)]]),
      firstPageComplete: true,
    },
    isLoading: false,
    isError: false,
    isPlaceholderData: false,
    refetch: vi.fn(),
    ...extra,
  });
}

function discovery(results: Array<{ title: string }>) {
  mockUseCanRequest.mockReturnValue({
    discoveryEnabled: true,
    isResolving: false,
    submitDisabledReason: null,
  });
  mockUseRequestSearch.mockReturnValue({
    data: {
      page: 1,
      total_pages: 1,
      total_results: results.length,
      results: results.map((r, i) => ({
        media_type: "movie",
        tmdb_id: i + 1,
        title: r.title,
        availability: "missing",
        request: { requestable: true },
      })),
    },
    isLoading: false,
    isFetching: false,
    isError: false,
  });
}

function renderSearch(query: string) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[`/catalog?source=query&${query}`]}>
        <Catalog />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("search results", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: false,
      submitDisabledReason: null,
    });
    mockUseRequestSearch.mockReturnValue({
      data: undefined,
      isLoading: false,
      isFetching: false,
      isError: false,
    });
    mockUsePersonSearch.mockReturnValue({ data: [], isLoading: false, isError: false });
    libraryResults(5);
  });

  // However many library results a search returns, requestable titles come
  // two rows in, not after the whole list. jsdom renders one grid column, so
  // two rows are two titles.
  it.each([
    ["relevance", "q=heat"],
    ["another sort", "q=heat&sort=title&order=asc"],
  ])("places requestable titles two rows into the results under %s", (_sort, query) => {
    discovery([{ title: "Missing Film" }]);
    renderSearch(query);
    // The third library title comes after the request row.
    const order = screen
      .getByText("Missing Film")
      .compareDocumentPosition(screen.getByText("Film 2"));
    expect(order & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("offers actor and director results with person links when no titles match", () => {
    libraryResults(0);
    mockUsePersonSearch.mockReturnValue({
      data: [{ id: "137101642343383042", name: "Tom Hanks", photo_url: "/actor.jpg" }],
      isLoading: false,
      isError: false,
    });
    renderSearch("q=heat");
    expect(screen.getByRole("link", { name: /Tom Hanks/ })).toHaveAttribute(
      "href",
      "/person/137101642343383042",
    );
  });

  it("keeps title results visible when people search fails and offers a retry", async () => {
    const refetch = vi.fn();
    mockUsePersonSearch.mockReturnValue({
      data: undefined,
      isLoading: false,
      isError: true,
      refetch,
    });
    renderSearch("q=heat");
    expect(screen.getByText("Could not load people results.")).toBeInTheDocument();
    expect(screen.getByText("Film 0")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Retry people search" }));
    expect(refetch).toHaveBeenCalledOnce();
  });

  it("shows request results as the answer when the library has none", () => {
    libraryResults(0);
    discovery([{ title: "Missing Film" }]);
    renderSearch("q=heat");
    const grid = screen.getByTestId("request-grid");
    expect(grid).toHaveAttribute("data-library-had-hits", "false");
    expect(grid).toHaveAttribute("data-library-results-known", "true");
  });

  it("hides stale titles when the library search fails, and does not call them missing", () => {
    libraryResults(1, { isError: true });
    discovery([{ title: "Missing Film" }]);
    mockUsePersonSearch.mockReturnValue({
      data: [{ id: "137101642343383042", name: "Tom Hanks" }],
      isLoading: false,
      isError: false,
    });
    renderSearch("q=heat");
    expect(screen.getByText("Could not load search results.")).toBeInTheDocument();
    expect(screen.queryByText("Film 0")).toBeNull();
    expect(screen.getByTestId("request-grid")).toHaveAttribute(
      "data-library-results-known",
      "false",
    );
    // People come from their own search, so they stay.
    expect(screen.getByRole("link", { name: /Tom Hanks/ })).toBeInTheDocument();
  });

  it("shows the overview when the request list does not apply to the scope", () => {
    discovery([{ title: "Missing Film" }]);
    renderSearch("q=heat&type=audiobook&view=requests");
    expect(screen.queryByTestId("request-grid")).toBeNull();
    expect(screen.queryByText("Missing Film")).toBeNull();
    expect(screen.getByText("Film 0")).toBeInTheDocument();
  });

  it("keeps the request grid when only a later TMDB page has titles to request", () => {
    libraryResults(0);
    discovery([]);
    mockUseRequestSearch.mockReturnValue({
      data: { page: 1, total_pages: 2, total_results: 30, results: [] },
      isLoading: false,
      isFetching: false,
      isError: false,
    });
    renderSearch("q=heat");
    expect(screen.getByTestId("request-grid")).toBeInTheDocument();
    expect(screen.queryByText("No items found.")).toBeNull();
  });

  it("waits for discovery before leaving a linked request view", () => {
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: false,
      isResolving: true,
      submitDisabledReason: null,
    });
    renderSearch("q=heat&view=requests");
    expect(mockUsePersonSearch).toHaveBeenCalledWith("heat", 20, false, "video");
    expect(screen.queryByText("Film 0")).toBeNull();
  });

  it("leaves a linked request view for a query too short to search", () => {
    mockUseCanRequest.mockReturnValue({
      discoveryEnabled: true,
      isResolving: false,
      submitDisabledReason: null,
    });
    renderSearch("q=x&view=requests");
    expect(screen.getByText("Film 0")).toBeInTheDocument();
  });

  it("says nothing was found once the library and discovery both come back empty", () => {
    libraryResults(0);
    discovery([]);
    renderSearch("q=heat");
    expect(screen.getByText("No items found.")).toBeInTheDocument();
  });

  it.each([
    ["applies the preferred scope when the URL has none", "q=heat", "video"],
    ["keeps an explicit type=all", "q=heat&type=all", undefined],
  ])("%s, with approximate totals", (_name, query, scope) => {
    renderSearch(query);
    expect(mockUseCatalogWindow).toHaveBeenCalledWith(
      expect.objectContaining({
        source: "query",
        q: "heat",
        query_definition: expect.objectContaining({ media_scope: scope }),
      }),
      expect.objectContaining({ includeTotal: false }),
    );
    expect(mockUsePersonSearch).toHaveBeenCalledWith("heat", 20, true, scope);
  });
});
