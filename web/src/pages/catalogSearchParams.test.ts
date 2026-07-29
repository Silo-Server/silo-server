import { describe, expect, it } from "vitest";

import {
  buildCatalogApiSearchParams,
  buildCatalogFilterSearchParams,
  buildCatalogQueryUpdateHref,
  buildCatalogHref,
  buildPersonCatalogHref,
  buildPersonalCatalogHref,
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

  it("keeps overlay params for collection sources", () => {
    const state = parseCatalogSearchParams(
      params("source=user_collection&collection_id=col-7&type=movie&genre=Drama&sort=title"),
    );

    expect(state.source).toBe("user_collection");
    expect(state.collection_id).toBe("col-7");
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

  it("parses the video group scope and treats type=all as unscoped", () => {
    const video = parseCatalogSearchParams(params("source=query&q=heat&type=video"));
    expect(video.query_definition.media_scope).toBe("video");

    const all = parseCatalogSearchParams(params("source=query&q=heat&type=all"));
    expect(all.query_definition.media_scope).toBeUndefined();
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

  it("defaults the watchlist to source order until a sort is chosen", () => {
    expect(parseCatalogSearchParams(params("source=watchlist")).uses_source_order).toBe(true);
    expect(parseCatalogSearchParams(params("source=watchlist&sort=title")).uses_source_order).toBe(
      false,
    );
    expect(
      parseCatalogSearchParams(params("source=watchlist&sort=added_at&order=desc"))
        .uses_source_order,
    ).toBe(false);
    // Other personal sources keep their existing behavior.
    expect(parseCatalogSearchParams(params("source=favorites")).uses_source_order).toBe(false);
    expect(parseCatalogSearchParams(params("source=history")).uses_source_order).toBe(false);
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
    expect(catalogSourceAllowsOverlay("library_collection")).toBe(true);
    expect(catalogSourceAllowsOverlay("user_collection")).toBe(true);
    expect(catalogSourceAllowsOverlay("section")).toBe(false);
  });
});

describe("buildCatalogHref", () => {
  it("preserves type=all when the query filter selects All Media", () => {
    const state = parseCatalogSearchParams(params("source=query&q=heat&type=video"));
    state.query_definition = {
      ...state.query_definition,
      media_scope: undefined,
    };

    const built = buildCatalogFilterSearchParams(state);

    expect(built.get("type")).toBe("all");
    expect(parseCatalogSearchParams(built).query_definition.media_scope).toBeUndefined();
  });

  it("preserves All Media and other filters when the live query changes", () => {
    const state = parseCatalogSearchParams(
      params("source=query&q=heat&type=all&genre=Drama&sort=title&order=asc"),
    );

    const href = buildCatalogQueryUpdateHref(state, "heater");
    const built = new URL(`http://example.test${href}`);

    expect(built.searchParams.get("q")).toBe("heater");
    expect(built.searchParams.get("type")).toBe("all");
    expect(built.searchParams.get("sort")).toBe("title");
    expect(built.searchParams.get("order")).toBe("asc");
    expect(parseCatalogSearchParams(built.searchParams).query_definition.groups).toContainEqual({
      match: "all",
      rules: [{ field: "genre", op: "contains", value: "Drama" }],
    });
  });

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

  it("omits the sort for watchlist source order but keeps an explicit Date Added", () => {
    const sourceOrdered = buildCatalogApiSearchParams({
      source: "watchlist",
      uses_source_order: true,
      query_definition: {
        library_ids: [],
        match: "all",
        groups: [],
        sort: { field: "added_at", order: "desc" },
      },
    });
    expect(sourceOrdered.toString()).toBe("source=watchlist");

    const explicitAddedAt = buildCatalogApiSearchParams({
      source: "watchlist",
      uses_source_order: false,
      query_definition: {
        library_ids: [],
        match: "all",
        groups: [],
        sort: { field: "added_at", order: "desc" },
      },
    });
    expect(explicitAddedAt.toString()).toBe("source=watchlist&sort=added_at&order=desc");
  });

  it("builds source-ordered personal catalog hrefs without a sort param", () => {
    expect(buildPersonalCatalogHref("watchlist")).toBe("/catalog?source=watchlist");
    expect(buildPersonalCatalogHref("favorites")).toBe("/catalog?source=favorites");
  });

  it("builds collection catalog URLs with overlay params", () => {
    const href = buildCatalogHref({
      source: "user_collection",
      collection_id: "col-7",
      title: "Shared Picks",
      query_definition: {
        library_ids: [3],
        match: "all",
        groups: [{ match: "all", rules: [{ field: "genre", op: "contains", value: "Drama" }] }],
        sort: { field: "title", order: "asc" },
      },
    });

    const built = new URL(`http://example.test${href}`);
    expect(built.pathname).toBe("/catalog");
    expect(built.searchParams.get("source")).toBe("user_collection");
    expect(built.searchParams.get("collection_id")).toBe("col-7");
    expect(built.searchParams.get("title")).toBe("Shared Picks");
    expect(built.searchParams.get("library_id")).toBe("3");
    expect(built.searchParams.get("sort")).toBe("title");
    expect(built.searchParams.get("order")).toBe("asc");
    expect(built.searchParams.get("groups[0][rules][0][field]")).toBe("genre");
    expect(built.searchParams.get("groups[0][rules][0][op]")).toBe("contains");
    expect(built.searchParams.get("groups[0][rules][0][value]")).toBe("Drama");
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
