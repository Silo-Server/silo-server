import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createEmptyQueryDefinition } from "@/api/types";

import CatalogFiltersPanel, { CatalogFilterSheetContainer } from "./CatalogFiltersPanel";

const mockUseCatalogFilters = vi.fn();

vi.mock("@/components/collections/CollectionGuidedRulesEditor", () => ({
  default: () => <div>Guided catalog editor</div>,
  queryDefinitionToGuidedState: () => ({
    mediaScope: "all",
    libraryIds: [],
    genres: [],
    decade: "",
    yearFrom: "",
    yearTo: "",
    minRating: "",
    contentRating: "",
    originalLanguages: [],
    actor: "",
    director: "",
    writer: "",
    producer: "",
    author: "",
    narrator: "",
    series: "",
    studio: "",
    network: "",
    country: "",
    status: "",
    watchStatus: "",
    addedInLast: "",
    releasedInLast: "",
    fourK: false,
    hdr: false,
    dolbyVision: false,
    sortField: "added_at",
    sortOrder: "desc",
  }),
  guidedStateToQueryDefinition: (_state: unknown, existing: unknown) => existing,
}));

vi.mock("@/components/collections/CollectionRulesEditor", () => ({
  default: () => <div>Advanced catalog editor</div>,
}));

vi.mock("./CatalogFilterSheet", () => ({
  default: ({ filters }: { filters?: { genres?: string[] } }) => (
    <div>Sheet filters: {(filters?.genres ?? []).join(",")}</div>
  ),
}));

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogFilters: (...args: unknown[]) => mockUseCatalogFilters(...args),
}));

describe("CatalogFiltersPanel", () => {
  beforeEach(() => {
    mockUseCatalogFilters.mockReset();
    mockUseCatalogFilters.mockReturnValue({
      data: { genres: ["Scoped Drama"] },
      isLoading: false,
    });
  });

  it("hides overlay controls for exact sources", () => {
    for (const state of [
      {
        source: "section" as const,
        scope: "home" as const,
        section_id: "sec-1",
      },
      {
        source: "library_collection" as const,
        collection_id: "col-1",
      },
      {
        source: "user_collection" as const,
        collection_id: "col-2",
      },
    ]) {
      const markup = renderToStaticMarkup(
        <CatalogFiltersPanel
          state={{
            ...state,
            query_definition: createEmptyQueryDefinition(),
          }}
          onStateChange={() => {}}
        />,
      );

      expect(markup).toContain("Filters are locked to this source");
      expect(markup).not.toContain("Guided catalog editor");
      expect(markup).not.toContain("Advanced catalog editor");
    }
  });

  it("renders filter bar for overlay-capable personal sources", () => {
    const markup = renderToStaticMarkup(
      <CatalogFiltersPanel
        state={{
          source: "favorites",
          query_definition: createEmptyQueryDefinition(),
        }}
        onStateChange={() => {}}
      />,
    );

    expect(markup).toContain("Filters");
    expect(markup).not.toContain("Filters are locked to this source");
  });

  it("loads scoped filters through the catalog query hook", () => {
    const markup = renderToStaticMarkup(
      <CatalogFilterSheetContainer
        state={{
          source: "query",
          q: "house",
          query_definition: createEmptyQueryDefinition(),
        }}
        open
        onOpenChange={() => {}}
        guidedState={{
          mediaScope: "all",
          libraryIds: [],
          genres: [],
          decade: "",
          yearFrom: "",
          yearTo: "",
          minRating: "",
          contentRating: "",
          originalLanguages: [],
          actor: "",
          director: "",
          writer: "",
          producer: "",
          author: "",
          narrator: "",
          series: "",
          studio: "",
          network: "",
          country: "",
          status: "",
          watchStatus: "",
          addedInLast: "",
          releasedInLast: "",
          fourK: false,
          hdr: false,
          dolbyVision: false,
          sortField: "added_at",
          sortOrder: "desc",
        }}
        onUpdate={() => {}}
        libraries={[]}
        allowLibrarySelection={false}
        allowPersonalizedFilters
        allowPersonalizedSorts
        editorMode="guided"
        onEditorModeChange={() => {}}
        queryDefinition={createEmptyQueryDefinition()}
        onQueryDefinitionChange={() => {}}
      />,
    );

    expect(mockUseCatalogFilters).toHaveBeenCalled();
    expect(markup).toContain("Sheet filters: Scoped Drama");
  });
});
