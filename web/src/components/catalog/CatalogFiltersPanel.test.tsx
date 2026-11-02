import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createEmptyQueryDefinition } from "@/api/types";

import CatalogFiltersPanel from "./CatalogFiltersPanel";

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

vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
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

  it("hides overlay controls for section sources", () => {
    const markup = renderToStaticMarkup(
      <CatalogFiltersPanel
        state={{
          source: "section",
          scope: "home",
          section_id: "sec-1",
          query_definition: createEmptyQueryDefinition(),
        }}
        onStateChange={() => {}}
      />,
    );

    expect(markup).toContain("Filters are locked to this source");
    expect(markup).not.toContain("Guided catalog editor");
    expect(markup).not.toContain("Advanced catalog editor");
  });
});
