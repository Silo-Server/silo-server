import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import libraryLayoutOk from "../../../contracts/api/v2/fixtures/get_library_layout_ok.json";
import queryCatalogItemsOk from "../../../contracts/api/v2/fixtures/query_catalog_items_ok.json";
import { V2ProblemError, V2TimeoutError } from "@/api/v2/request";

const mocks = vi.hoisted(() => ({
  v2: vi.fn(),
  fetchLibrarySectionItems: vi.fn(),
  visibleEnd: null as number | null,
}));

// The real query hooks run against a stubbed request boundary, so the pages
// see the same query states a failed or recovered server produces.
vi.mock("@/api/v2/request", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/request")>()),
  v2: mocks.v2,
}));

vi.mock("@/hooks/queries/sections", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/sections")>()),
  fetchLibrarySectionItems: mocks.fetchLibrarySectionItems,
}));

vi.mock("@/hooks/queries/sidebarPins", () => ({
  useSidebarPins: () => ({ pins: {} }),
}));

// Stands in for the grid with its empty message, which a failed browse must
// never reach.
vi.mock("@/components/ItemGrid", async () => {
  const { useEffect } = await import("react");
  return {
    default: function ItemGridStandIn({
      totalItems,
      loading,
      onVisibleRangeChange,
    }: {
      totalItems: number;
      loading?: boolean;
      onVisibleRangeChange?: (start: number, end: number) => void;
    }) {
      // Scrolls the grid to the range a test asks for, as the real grid
      // reports what is on screen.
      useEffect(() => {
        if (mocks.visibleEnd !== null) onVisibleRangeChange?.(0, mocks.visibleEnd);
      }, [onVisibleRangeChange]);
      return loading ? (
        <div>Loading grid</div>
      ) : totalItems === 0 ? (
        <div>No items found.</div>
      ) : (
        <div>{`Grid of ${totalItems}`}</div>
      );
    },
  };
});

vi.mock("@/components/catalog/CatalogFiltersPanel", () => ({
  default: () => <div>Filters</div>,
}));

vi.mock("@/components/ScrollToTopButton", () => ({
  default: () => null,
}));

vi.mock("@/components/SectionRow", () => ({
  default: ({ section }: { section: { title: string } }) => <div>{`Row ${section.title}`}</div>,
}));

vi.mock("@/components/MediaCarousel", () => ({
  default: ({ title }: { title: string }) => <div>{`Loading ${title}`}</div>,
}));

vi.mock("@/components/HeroBanner", () => ({
  default: () => <div>Hero</div>,
}));

import LibraryBrowse from "./LibraryBrowse";
import LibraryRecommended from "./LibraryRecommended";

function renderWithClient(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/library/7"]}>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
  return client;
}

function renderBrowse() {
  return renderWithClient(
    <LibraryBrowse
      libraryId={7}
      libraryType="mixed"
      browseType="series"
      queryDefinition={{
        library_ids: [],
        media_scope: "movie",
        match: "all",
        groups: [],
        sort: { field: "title", order: "asc" },
      }}
      onBrowseTypeChange={() => {}}
      onQueryDefinitionChange={() => {}}
    />,
  );
}

function callsTo(key: string) {
  return mocks.v2.mock.calls.filter(([called]) => called === key).length;
}

function invalidFilter() {
  return new V2ProblemError("queryCatalogItems", {
    type: "https://siloserver.org/docs/api/v2/problems/validation_failed",
    title: "Validation failed",
    status: 422,
    detail: "The filter is not valid.",
    instance: "/api/v2/catalog/query",
  });
}

afterEach(() => {
  mocks.v2.mockReset();
  mocks.fetchLibrarySectionItems.mockReset();
  mocks.visibleEnd = null;
});

describe("Library tab load errors", () => {
  it("shows the load error instead of an empty grid and retries the browse", async () => {
    mocks.v2
      .mockRejectedValueOnce(new V2TimeoutError("queryCatalogItems", 30_000))
      .mockResolvedValue(queryCatalogItemsOk);
    renderBrowse();

    expect(
      await screen.findByRole("heading", { name: "Couldn't load this library" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("The server isn't responding. Try again in a moment."),
    ).toBeInTheDocument();
    expect(screen.queryByText("No items found.")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByText("Grid of 3")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load this library")).not.toBeInTheDocument();
    expect(callsTo("POST /api/v2/catalog/query")).toBe(2);
  });

  it("does not present a refused query as an empty library", async () => {
    mocks.v2.mockRejectedValue(invalidFilter());
    renderBrowse();

    expect(
      await screen.findByRole("heading", { name: "Couldn't load this library" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Something went wrong while loading it. Try again in a moment."),
    ).toBeInTheDocument();
    expect(screen.queryByText("No items found.")).not.toBeInTheDocument();
  });

  it("keeps the loaded grid when a background refetch fails", async () => {
    mocks.v2
      .mockResolvedValueOnce(queryCatalogItemsOk)
      .mockRejectedValue(new V2TimeoutError("queryCatalogItems", 30_000));
    const client = renderBrowse();
    expect(await screen.findByText("Grid of 3")).toBeInTheDocument();

    // A refresh after playback or a reconnect refetches the browse.
    await act(() => client.refetchQueries());

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Couldn't refresh this library. The server isn't responding.",
    );
    expect(screen.getByText("Grid of 3")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load this library")).not.toBeInTheDocument();
    expect(screen.queryByText("No items found.")).not.toBeInTheDocument();
  });

  it("keeps the loaded grid when a later visible page fails, and retries that page", async () => {
    // Two pages of results; the second is on screen and its request fails once.
    const firstPage = {
      ...queryCatalogItemsOk,
      total: 120,
      page: { has_more: true, next_cursor: "after-page-0" },
    };
    let laterPageFailures = 0;
    mocks.v2.mockImplementation((_key: string, options: { body: { seek?: number } }) => {
      if (options.body.seek === undefined) return Promise.resolve(firstPage);
      laterPageFailures += 1;
      return laterPageFailures === 1
        ? Promise.reject(new TypeError("Failed to fetch"))
        : Promise.resolve({ ...firstPage, page: { has_more: false } });
    });
    mocks.visibleEnd = 119;
    renderBrowse();

    expect(await screen.findByRole("alert")).toHaveTextContent("Some items couldn't be loaded.");
    expect(screen.getByText("Grid of 120")).toBeInTheDocument();
    expect(screen.queryByText("Couldn't load this library")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(screen.getByText("Grid of 120")).toBeInTheDocument();
    expect(laterPageFailures).toBe(2);
  });

  it("renders the grid when the browse succeeds", async () => {
    mocks.v2.mockResolvedValue(queryCatalogItemsOk);
    renderBrowse();

    expect(await screen.findByText("Grid of 3")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });
});

describe("Recommended tab load errors", () => {
  function recommendedSection() {
    return {
      section: {
        ...libraryLayoutOk.sections[1],
        total_count: 0,
        items: [{ content_id: "movie-1", title: "Heat", type: "movie" }],
      },
    };
  }

  it("shows the load error when the layout fails and retries it", async () => {
    mocks.v2
      .mockRejectedValueOnce(new V2TimeoutError("getLibraryLayout", 30_000))
      .mockResolvedValue(libraryLayoutOk);
    mocks.fetchLibrarySectionItems.mockResolvedValue(recommendedSection());
    renderWithClient(<LibraryRecommended libraryId={7} libraryType="movies" />);

    expect(
      await screen.findByRole("heading", { name: "Couldn't load recommendations" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("The server isn't responding. Try again in a moment."),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() =>
      expect(screen.queryByText("Couldn't load recommendations")).not.toBeInTheDocument(),
    );
    expect(await screen.findAllByText("Row Recently Added")).not.toHaveLength(0);
    expect(callsTo("GET /api/v2/library/{id}/layout")).toBe(2);
  });

  it("keeps the cached layout when a refetch fails and retries the layout", async () => {
    mocks.v2
      .mockResolvedValueOnce(libraryLayoutOk)
      .mockRejectedValueOnce(new V2TimeoutError("getLibraryLayout", 30_000))
      .mockResolvedValue(libraryLayoutOk);
    mocks.fetchLibrarySectionItems.mockResolvedValue(recommendedSection());
    const client = renderWithClient(<LibraryRecommended libraryId={7} libraryType="movies" />);
    expect(await screen.findAllByText("Row Recently Added")).not.toHaveLength(0);

    // A refresh after playback or a reconnect refetches the layout.
    await act(() => client.refetchQueries({ queryKey: ["sections", "library", 7, "layout"] }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Couldn't refresh recommendations. The server isn't responding.",
    );
    expect(screen.getAllByText("Row Recently Added")).not.toHaveLength(0);
    expect(screen.queryByText("Couldn't load recommendations")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(screen.getAllByText("Row Recently Added")).not.toHaveLength(0);
    expect(callsTo("GET /api/v2/library/{id}/layout")).toBe(3);
  });

  it("renders the sections when the layout loads", async () => {
    mocks.v2.mockResolvedValue(libraryLayoutOk);
    mocks.fetchLibrarySectionItems.mockResolvedValue(recommendedSection());
    renderWithClient(<LibraryRecommended libraryId={7} libraryType="movies" />);

    expect(await screen.findAllByText("Row Recently Added")).not.toHaveLength(0);
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });
});
