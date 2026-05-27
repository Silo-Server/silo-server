import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";

const mocks = vi.hoisted(() => ({
  useCatalogWindow: vi.fn(),
}));

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogWindow: (...args: unknown[]) => mocks.useCatalogWindow(...args),
}));

vi.mock("@/components/ItemGrid", () => ({
  default: () => <div>Grid</div>,
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
  });

  it("loads library browse data through the catalog query hook", () => {
    renderToStaticMarkup(
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

    expect(mocks.useCatalogWindow).toHaveBeenCalled();
  });

  it("shows the filtered item count from the catalog result", () => {
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

    expect(markup).toContain("1,234 items");
  });

  it("shows a loading state for the item count while catalog data loads", () => {
    mocks.useCatalogWindow.mockReturnValue({
      data: {
        totalItems: 0,
        pages: new Map(),
      },
      isLoading: true,
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

    expect(markup).toContain("Loading item count");
    expect(markup).not.toContain("0 items");
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
});
