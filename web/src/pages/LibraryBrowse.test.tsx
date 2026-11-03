import { isValidElement, type ComponentProps, type ReactElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { QueryDefinition } from "@/api/types";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";

const mocks = vi.hoisted(() => ({
  useCatalogWindow: vi.fn(),
  useLibraryHasItems: vi.fn(),
  useCatalogLetters: vi.fn(),
  itemGridEmptyState: undefined as ReactNode,
  itemGridTotalItems: undefined as number | undefined,
}));

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: (...args: unknown[]) => mocks.useCatalogWindow(...args),
  useLibraryHasItems: (...args: unknown[]) => mocks.useLibraryHasItems(...args),
  useCatalogLetters: (...args: unknown[]) => mocks.useCatalogLetters(...args),
}));

vi.mock("@/components/ItemGrid", () => ({
  default: ({
    totalItems,
    loading,
    emptyState,
  }: {
    totalItems: number;
    loading?: boolean;
    emptyState?: ReactNode;
  }) => {
    mocks.itemGridEmptyState = emptyState;
    mocks.itemGridTotalItems = totalItems;
    if (loading) return <div>Grid loading</div>;
    if (totalItems === 0) return <div>{emptyState ?? "No items found."}</div>;
    return <div>Grid</div>;
  },
}));

vi.mock("@/components/LibraryEmptyState", () => ({
  default: ({ libraryId }: { libraryId: number }) => <div>Library {libraryId} is empty</div>,
}));

vi.mock("@/components/ScrollToTopButton", () => ({
  default: () => <div>Scroll</div>,
}));

vi.mock("@/components/catalog/CatalogFiltersPanel", () => ({
  default: ({
    resultCountLabel,
    resultCountLoading,
  }: {
    resultCountLabel?: string;
    resultCountLoading?: boolean;
  }) => (
    <div>
      Filters
      {resultCountLoading ? <span>Loading item count</span> : null}
      {!resultCountLoading && resultCountLabel ? <span>{resultCountLabel}</span> : null}
    </div>
  ),
}));

import LibraryBrowse from "./LibraryBrowse";

type BrowseProps = ComponentProps<typeof LibraryBrowse>;

/** Renders a movie library's series browse; `props` override any of it. */
function renderBrowse(props: Partial<BrowseProps> & Pick<BrowseProps, "queryDefinition">) {
  return renderToStaticMarkup(
    <LibraryBrowse
      libraryId={7}
      libraryType="movie"
      browseType="series"
      onBrowseTypeChange={() => {}}
      onQueryDefinitionChange={() => {}}
      {...props}
    />,
  );
}

/** The search state of the latest catalog window request. */
function lastWindowState() {
  return mocks.useCatalogWindow.mock.calls.at(-1)![0] as CatalogSearchState;
}

// A fresh query each call, so one test's normalization can't reach another's input.
const lastAiredFirst = (): QueryDefinition => ({
  library_ids: [],
  match: "all",
  groups: [],
  sort: { field: "last_air_date", order: "desc" },
});

describe("LibraryBrowse", () => {
  beforeEach(() => {
    mocks.useCatalogWindow.mockReset();
    mocks.useCatalogWindow.mockReturnValue({
      data: {
        totalItems: 1,
        pages: new Map([[0, [{ content_id: "movie-1", title: "Heat", type: "movie" }]]]),
      },
      isLoading: false,
    });
    mocks.useLibraryHasItems.mockReset();
    mocks.useLibraryHasItems.mockReturnValue({ data: undefined, isLoading: false });
    mocks.useCatalogLetters.mockReset();
    mocks.useCatalogLetters.mockReturnValue({ data: undefined });
    mocks.itemGridEmptyState = undefined;
    mocks.itemGridTotalItems = undefined;
  });

  describe("letter bar", () => {
    const render = (sort: QueryDefinition["sort"]) =>
      renderBrowse({ queryDefinition: { library_ids: [], match: "all", groups: [], sort } });

    it("shows in title order and sizes the grid to the index's exact total", () => {
      mocks.useCatalogLetters.mockReturnValue({
        data: {
          total: 640,
          letters: [
            { letter: "#", count: 40, position: 0 },
            { letter: "H", count: 600, position: 40 },
          ],
          runs: [
            { letter: "#", position: 0 },
            { letter: "H", position: 40 },
          ],
        },
      });

      const markup = render({ field: "title", order: "asc" });

      expect(mocks.useCatalogLetters).toHaveBeenCalledWith(
        expect.objectContaining({ library_id: 7 }),
        { enabled: true },
      );
      expect(markup).toContain('aria-label="Jump to letter"');
      expect(markup).toContain('aria-label="H, 600 titles"');
      expect(markup).toContain('aria-label="Q, no titles"');
      expect(mocks.itemGridTotalItems).toBe(640);
    });

    it("sizes the grid past the pager's estimated runway to the exact total", () => {
      // A full first page with more to come: the pager guesses five pages of
      // runway, but the index knows there are only 100 titles.
      mocks.useCatalogWindow.mockReturnValue({
        data: { totalItems: 360, knownItems: 60, pages: new Map([[0, []]]) },
        isLoading: false,
      });
      mocks.useCatalogLetters.mockReturnValue({
        data: {
          total: 100,
          letters: [{ letter: "A", count: 100, position: 0 }],
          runs: [{ letter: "A", position: 0 }],
        },
      });

      render({ field: "title", order: "asc" });

      expect(mocks.itemGridTotalItems).toBe(100);
    });

    it("keeps the bar and says so when the index fails to refresh", () => {
      const refetch = vi.fn(async () => undefined);
      mocks.useCatalogLetters.mockReturnValue({
        data: {
          total: 1,
          letters: [{ letter: "H", count: 1, position: 0 }],
          runs: [{ letter: "H", position: 0 }],
        },
        isRefetchError: true,
        error: new Error("boom"),
        refetch,
      });

      const markup = render({ field: "title", order: "asc" });

      expect(markup).toContain('aria-label="Jump to letter"');
      expect(markup).toContain("Couldn&#x27;t refresh the letter bar");
    });

    it("stays away and asks for nothing in other orders", () => {
      const markup = render({ field: "added_at", order: "desc" });

      expect(mocks.useCatalogLetters).toHaveBeenCalledWith(expect.anything(), { enabled: false });
      expect(markup).not.toContain("Jump to letter");
      expect(mocks.itemGridTotalItems).toBe(1);
    });

    it("steps aside when the index can't be read", () => {
      mocks.useCatalogLetters.mockReturnValue({ data: undefined, isError: true });

      expect(render({ field: "title", order: "asc" })).not.toContain("Jump to letter");
    });
  });

  it("does not show the estimated item count as an exact result count", () => {
    mocks.useCatalogWindow.mockReturnValue({
      data: {
        totalItems: 1234,
        pages: new Map(),
      },
      isLoading: false,
    });

    const markup = renderBrowse({
      libraryType: "mixed",
      queryDefinition: {
        library_ids: [],
        media_scope: "movie",
        match: "all",
        groups: [],
        sort: { field: "title", order: "asc" },
      },
    });

    expect(markup).toContain("Filters");
    expect(markup).toContain("Grid");
    expect(mocks.useLibraryHasItems).toHaveBeenCalledWith(7, { enabled: false });
    expect(mocks.itemGridEmptyState).toBeUndefined();
    expect(markup).not.toContain("1,234 items");
  });

  it("requests episode browse mode through a type override and normalizes series-only sorts", () => {
    renderBrowse({
      libraryType: "series",
      browseType: "episode",
      queryDefinition: lastAiredFirst(),
    });

    const state = lastWindowState();
    expect(state.type_override).toBe("episode");
    expect(state.query_definition.sort).toEqual({ field: "title", order: "asc" });
  });

  it("normalizes video-only sorts away through the shared ebook relevance scope", () => {
    renderBrowse({ libraryId: 11, libraryType: "ebooks", queryDefinition: lastAiredFirst() });

    const state = lastWindowState();
    expect(state.library_id).toBe(11);
    expect(state.query_definition.media_scope).toBe("ebook");
    expect(state.query_definition.sort).toEqual({ field: "title", order: "asc" });
  });

  describe("empty results", () => {
    const filteredQuery: QueryDefinition = {
      library_ids: [],
      match: "all",
      groups: [{ match: "all", rules: [{ field: "genre", op: "is", value: "Western" }] }],
      sort: { field: "title", order: "asc" },
    };
    const unfilteredQuery: QueryDefinition = {
      library_ids: [],
      match: "all",
      groups: [],
      sort: { field: "title", order: "asc" },
    };

    beforeEach(() => {
      mocks.useCatalogWindow.mockReturnValue({
        data: { totalItems: 0, pages: new Map() },
        isLoading: false,
        isError: false,
      });
    });

    it("says the library is empty when it holds no items at all", () => {
      mocks.useLibraryHasItems.mockReturnValue({ data: false, isLoading: false });

      const markup = renderBrowse({ queryDefinition: unfilteredQuery });

      expect(mocks.useLibraryHasItems).toHaveBeenCalledWith(7, { enabled: true });
      expect(markup).toContain("Library 7 is empty");
      expect(markup).not.toContain("No items found.");
      expect(markup).not.toContain("match your current filters");
    });

    it("keeps a filter message and a clear action when filters match nothing", () => {
      mocks.useLibraryHasItems.mockReturnValue({ data: true, isLoading: false });
      const onQueryDefinitionChange = vi.fn();

      const markup = renderBrowse({ queryDefinition: filteredQuery, onQueryDefinitionChange });

      expect(markup).toContain("No items match your current filters.");
      expect(markup).toContain("Clear filters");
      expect(markup).not.toContain("is empty");

      const emptyState = mocks.itemGridEmptyState;
      expect(isValidElement(emptyState)).toBe(true);
      (emptyState as ReactElement<{ onClearFilters?: () => void }>).props.onClearFilters?.();
      expect(onQueryDefinitionChange).toHaveBeenCalledWith(
        expect.objectContaining({ groups: [], match: "all", sort: filteredQuery.sort }),
      );
    });

    it("stays in the loading state while the library check is pending", () => {
      mocks.useLibraryHasItems.mockReturnValue({ data: undefined, isLoading: true });

      const markup = renderBrowse({ queryDefinition: filteredQuery });

      expect(markup).toContain("Grid loading");
      expect(markup).not.toContain("is empty");
    });
  });
});
