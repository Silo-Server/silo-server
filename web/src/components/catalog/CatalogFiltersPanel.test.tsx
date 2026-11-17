import { render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createEmptyQueryDefinition } from "@/api/types";

import CatalogFiltersPanel from "./CatalogFiltersPanel";

const mockUseCatalogFilters = vi.fn();

vi.mock("@/components/collections/CollectionGuidedRulesEditor", () => ({
  default: () => <div>Guided catalog editor</div>,
  queryDefinitionToGuidedState: (qd?: { sort?: { field?: string; order?: string } }) => ({
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
    sortField: qd?.sort?.field ?? "added_at",
    sortOrder: qd?.sort?.order ?? "desc",
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

  // A text search ranks by relevance until the viewer picks another sort, so
  // the sort menu has to name it, and offer it again after another pick.
  it.each([
    ["no sort chosen", undefined, false, "Relevance"],
    [
      "relevance in the URL",
      { field: "relevance" as const, order: "desc" as const },
      true,
      "Relevance",
    ],
    ["another sort chosen", { field: "title" as const, order: "asc" as const }, true, "Title"],
  ])("shows the search sort with %s", (_case, sort, explicit, expected) => {
    render(
      <CatalogFiltersPanel
        relevanceSort
        state={{
          source: "query",
          scope: "home",
          q: "the hobbit",
          explicit_sort: explicit,
          query_definition: { ...createEmptyQueryDefinition(), ...(sort ? { sort } : {}) },
        }}
        onStateChange={() => {}}
      />,
    );
    const labels = screen.getAllByRole("combobox").map((box) => box.textContent);
    expect(labels).toContain(expected);
  });

  // Date Added is also the placeholder a sortless search carries, so picking it
  // from Relevance must still count as a choice.
  it("applies Date Added picked from the Relevance default", async () => {
    // The open list scrolls its selected item into view; jsdom has no scrolling.
    Element.prototype.scrollIntoView ??= vi.fn();
    const user = (await import("@testing-library/user-event")).default.setup();
    const onStateChange = vi.fn();
    render(
      <CatalogFiltersPanel
        relevanceSort
        state={{
          source: "query",
          scope: "home",
          q: "the hobbit",
          explicit_sort: false,
          query_definition: createEmptyQueryDefinition(),
        }}
        onStateChange={onStateChange}
      />,
    );
    const sort = screen.getAllByRole("combobox").find((box) => box.textContent === "Relevance");
    sort!.focus();
    await user.keyboard("{Enter}");
    await user.click(await screen.findByRole("option", { name: "Date Added" }));
    expect(onStateChange).toHaveBeenCalledWith(
      expect.objectContaining({
        explicit_sort: true,
        query_definition: expect.objectContaining({ sort: { field: "added_at", order: "desc" } }),
      }),
    );
  });
});
