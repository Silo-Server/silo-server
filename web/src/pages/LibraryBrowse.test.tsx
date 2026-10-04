import { isValidElement, type ReactElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { QueryDefinition } from "@/api/types";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";

const mocks = vi.hoisted(() => ({
  useCatalogWindow: vi.fn(),
  useLibraryHasItems: vi.fn(),
  itemGridEmptyState: undefined as ReactNode,
}));

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: (...args: unknown[]) => mocks.useCatalogWindow(...args),
  useLibraryHasItems: (...args: unknown[]) => mocks.useLibraryHasItems(...args),
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
    mocks.itemGridEmptyState = undefined;
  });

  it("does not show the estimated item count as an exact result count", () => {
    mocks.useCatalogWindow.mockReturnValue({
      data: {
        totalItems: 1234,
        pages: new Map(),
      },
      isLoading: false,
    });

    const markup = renderToStaticMarkup(
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

    expect(markup).toContain("Filters");
    expect(markup).toContain("Grid");
    expect(mocks.useLibraryHasItems).toHaveBeenCalledWith(7, { enabled: false });
    expect(mocks.itemGridEmptyState).toBeUndefined();
    expect(markup).not.toContain("1,234 items");
  });

  it("maps last_air_date into the catalog query definition", () => {
    renderToStaticMarkup(
      <LibraryBrowse
        libraryId={7}
        libraryType="series"
        browseType="series"
        queryDefinition={{
          library_ids: [],
          match: "all",
          groups: [],
          sort: { field: "last_air_date", order: "desc" },
        }}
        onBrowseTypeChange={() => {}}
        onQueryDefinitionChange={() => {}}
      />,
    );

    expect(mocks.useCatalogWindow).toHaveBeenCalledWith(
      expect.objectContaining({
        query_definition: expect.objectContaining({
          sort: { field: "last_air_date", order: "desc" },
        }),
      }),
      expect.objectContaining({
        includeTotal: false,
      }),
    );
  });

  it("requests episode browse mode through a type override and normalizes series-only sorts", () => {
    renderToStaticMarkup(
      <LibraryBrowse
        libraryId={7}
        libraryType="series"
        browseType="episode"
        queryDefinition={{
          library_ids: [],
          match: "all",
          groups: [],
          sort: { field: "last_air_date", order: "desc" },
        }}
        onBrowseTypeChange={() => {}}
        onQueryDefinitionChange={() => {}}
      />,
    );

    const [state] = mocks.useCatalogWindow.mock.calls[
      mocks.useCatalogWindow.mock.calls.length - 1
    ] as [CatalogSearchState, Record<string, unknown>];
    expect(state.type_override).toBe("episode");
    expect(state.query_definition.sort).toEqual({ field: "title", order: "asc" });
  });

  it("uses audiobook media scope for audiobook libraries", () => {
    renderToStaticMarkup(
      <LibraryBrowse
        libraryId={10}
        libraryType="audiobooks"
        browseType="books"
        queryDefinition={{
          library_ids: [],
          match: "all",
          groups: [],
          sort: { field: "title", order: "asc" },
        }}
        onBrowseTypeChange={() => {}}
        onQueryDefinitionChange={() => {}}
      />,
    );

    const [state] = mocks.useCatalogWindow.mock.calls[
      mocks.useCatalogWindow.mock.calls.length - 1
    ] as [CatalogSearchState, Record<string, unknown>];
    expect(state.library_id).toBe(10);
    expect(state.query_definition.media_scope).toBe("audiobook");
  });

  it("normalizes video-only sorts away through the shared ebook relevance scope", () => {
    renderToStaticMarkup(
      <LibraryBrowse
        libraryId={11}
        libraryType="ebooks"
        browseType="series"
        queryDefinition={{
          library_ids: [],
          match: "all",
          groups: [],
          sort: { field: "last_air_date", order: "desc" },
        }}
        onBrowseTypeChange={() => {}}
        onQueryDefinitionChange={() => {}}
      />,
    );

    const [state] = mocks.useCatalogWindow.mock.calls[
      mocks.useCatalogWindow.mock.calls.length - 1
    ] as [CatalogSearchState, Record<string, unknown>];
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

    function renderBrowse(
      queryDefinition: QueryDefinition,
      onQueryDefinitionChange: (next: QueryDefinition) => void = () => {},
    ) {
      return renderToStaticMarkup(
        <LibraryBrowse
          libraryId={7}
          libraryType="movie"
          browseType="series"
          queryDefinition={queryDefinition}
          onBrowseTypeChange={() => {}}
          onQueryDefinitionChange={onQueryDefinitionChange}
        />,
      );
    }

    beforeEach(() => {
      mocks.useCatalogWindow.mockReturnValue({
        data: { totalItems: 0, pages: new Map() },
        isLoading: false,
        isError: false,
      });
    });

    it("says the library is empty when it holds no items at all", () => {
      mocks.useLibraryHasItems.mockReturnValue({ data: false, isLoading: false });

      const markup = renderBrowse(unfilteredQuery);

      expect(mocks.useLibraryHasItems).toHaveBeenCalledWith(7, { enabled: true });
      expect(markup).toContain("Library 7 is empty");
      expect(markup).not.toContain("No items found.");
      expect(markup).not.toContain("match your current filters");
    });

    it("keeps a filter message and a clear action when filters match nothing", () => {
      mocks.useLibraryHasItems.mockReturnValue({ data: true, isLoading: false });
      const onQueryDefinitionChange = vi.fn();

      const markup = renderBrowse(filteredQuery, onQueryDefinitionChange);

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

    it("does not offer to clear filters when none are set", () => {
      mocks.useLibraryHasItems.mockReturnValue({ data: true, isLoading: false });

      const markup = renderBrowse(unfilteredQuery);

      expect(markup).toContain("No items match your current filters.");
      expect(markup).not.toContain("Clear filters");
    });

    it("stays in the loading state while the library check is pending", () => {
      mocks.useLibraryHasItems.mockReturnValue({ data: undefined, isLoading: true });

      const markup = renderBrowse(filteredQuery);

      expect(markup).toContain("Grid loading");
      expect(markup).not.toContain("is empty");
    });
  });
});
