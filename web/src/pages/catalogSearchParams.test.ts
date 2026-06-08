import { describe, expect, it } from "vitest";

import {
  buildCatalogApiSearchParams,
  buildCatalogHref,
  buildPersonCatalogHref,
  catalogSourceAllowsOverlay,
  parseCatalogSearchParams,
} from "./catalogSearchParams";

function params(search: string) {
  return new URLSearchParams(search);
}

describe("parseCatalogSearchParams", () => {
  it("blocks overlay params for exact section sources", () => {
    const state = parseCatalogSearchParams(
      params("source=section&scope=home&section_id=sec-1&genre=Drama&sort=title"),
    );

    expect(state.source).toBe("section");
    expect(state.section_id).toBe("sec-1");
    expect(state.scope).toBe("home");
    expect(state.query_definition.groups).toEqual([]);
    expect(state.query_definition.sort).toEqual({ field: "added_at", order: "desc" });
  });

  it("keeps overlay params for favorites sources", () => {
    const state = parseCatalogSearchParams(
      params("source=favorites&type=movie&genre=Drama&sort=title&order=asc"),
    );

    expect(state.source).toBe("favorites");
    expect(state.query_definition.media_scope).toBe("movie");
    expect(state.query_definition.groups).toContainEqual({
      match: "all",
      rules: [{ field: "genre", op: "contains", value: "Drama" }],
    });
    expect(state.query_definition.sort).toEqual({ field: "title", order: "asc" });
  });

  it("keeps ebook media scope from catalog URLs", () => {
    const state = parseCatalogSearchParams(params("source=query&type=ebook&sort=author"));

    expect(state.query_definition.media_scope).toBe("ebook");
    expect(state.query_definition.sort).toEqual({ field: "author", order: "asc" });
  });

  it("parses person catalog sources with overlays", () => {
    const state = parseCatalogSearchParams(
      params(
        "source=person&person_id=117290402172239876&type=movie&genre=Drama&sort=title&order=asc",
      ),
    );

    expect(state.source).toBe("person");
    expect(state.person_id).toBe("117290402172239876");
    expect(state.query_definition.media_scope).toBe("movie");
    expect(state.query_definition.groups).toContainEqual({
      match: "all",
      rules: [{ field: "genre", op: "contains", value: "Drama" }],
    });
    expect(state.query_definition.sort).toEqual({ field: "title", order: "asc" });
  });

  it("normalizes legacy sort aliases in catalog URLs", () => {
    expect(
      parseCatalogSearchParams(params("source=query&sort=sort_title")).query_definition.sort,
    ).toEqual({ field: "title", order: "asc" });
    expect(
      parseCatalogSearchParams(params("source=query&sort=recently_added")).query_definition.sort,
    ).toEqual({ field: "added_at", order: "desc" });
    expect(
      parseCatalogSearchParams(params("source=query&sort=rating")).query_definition.sort,
    ).toEqual({ field: "rating_imdb", order: "desc" });
  });
});

describe("catalogSourceAllowsOverlay", () => {
  it("returns true only for supported overlay sources", () => {
    expect(catalogSourceAllowsOverlay("query")).toBe(true);
    expect(catalogSourceAllowsOverlay("favorites")).toBe(true);
    expect(catalogSourceAllowsOverlay("watchlist")).toBe(true);
    expect(catalogSourceAllowsOverlay("history")).toBe(true);
    expect(catalogSourceAllowsOverlay("section")).toBe(false);
    expect(catalogSourceAllowsOverlay("library_collection")).toBe(false);
    expect(catalogSourceAllowsOverlay("user_collection")).toBe(false);
  });
});

describe("buildCatalogHref", () => {
  it("keeps explicit added-at sorting in query-source URLs", () => {
    expect(
      buildCatalogApiSearchParams({
        source: "query",
        library_id: 2,
        query_definition: {
          library_ids: [2],
          match: "all",
          groups: [],
          sort: { field: "added_at", order: "desc" },
        },
      }).toString(),
    ).toBe("source=query&library_id=2&sort=added_at&order=desc");
  });

  it("builds a stable exact-source catalog URL", () => {
    expect(
      buildCatalogHref({
        source: "user_collection",
        collection_id: "col-7",
        title: "Shared Picks",
        query_definition: {
          library_ids: [3],
          match: "all",
          groups: [{ match: "all", rules: [{ field: "genre", op: "contains", value: "Drama" }] }],
          sort: { field: "title", order: "asc" },
        },
      }),
    ).toBe("/catalog?source=user_collection&collection_id=col-7&title=Shared+Picks");
  });

  it("builds person catalog URLs with the person id", () => {
    expect(
      buildCatalogHref({
        source: "person",
        person_id: "117290402172239876",
        query_definition: {
          library_ids: [],
          match: "all",
          groups: [],
          sort: { field: "added_at", order: "desc" },
        },
      }),
    ).toBe("/catalog?source=person&person_id=117290402172239876");
  });

  it("builds canonical person catalog URLs from raw route ids", () => {
    expect(buildPersonCatalogHref("117290402172239876")).toBe("/person/117290402172239876");
  });
});
