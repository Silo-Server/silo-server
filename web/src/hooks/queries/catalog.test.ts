import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import queryCatalogItemsOk from "../../../../contracts/api/v2/fixtures/query_catalog_items_ok.json";

import getCatalogFiltersOk from "../../../../contracts/api/v2/fixtures/get_catalog_filters_ok.json";
import searchCatalogFacetOk from "../../../../contracts/api/v2/fixtures/search_catalog_facet_ok.json";

import { setProfileId } from "@/api/client";
import { createEmptyQueryDefinition } from "@/api/types";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { fetchCatalogFacetSearch, fetchCatalogFilters, fetchCatalogPage } from "./catalog";

type FetchMock = ReturnType<typeof vi.fn<typeof fetch>>;

function stubFetch(body: unknown): FetchMock {
  const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(body));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function requestedUrl(fetchMock: FetchMock): URL {
  return new URL(String(fetchMock.mock.calls[0]?.[0]), "http://localhost");
}

describe("catalog browse and facets on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("fetches query-source facets and flattens the technical block", async () => {
    const fetchMock = stubFetch(getCatalogFiltersOk);

    const filters = await fetchCatalogFilters({
      source: "query",
      query_definition: createEmptyQueryDefinition(),
    });

    const url = requestedUrl(fetchMock);
    expect(url.pathname).toBe("/api/v2/catalog/filters");
    expect(url.searchParams.get("source")).toBe("query");
    expect(url.searchParams.has("skip_technical")).toBe(false);
    expect(filters.genres).toEqual(["Crime"]);
    expect(filters.authors).toEqual(["Frank Herbert"]);
    expect(filters.resolutions).toEqual(["2160p"]);
    expect(filters.subtitle_languages).toEqual(["en"]);
    expect(filters).not.toHaveProperty("technical");
  });

  it("asks for lightweight facets with skip_technical and never forwards the overlay", async () => {
    const fetchMock = stubFetch({ ...getCatalogFiltersOk, technical: undefined });

    await fetchCatalogFilters(
      {
        source: "query",
        q: "house",
        library_id: 7,
        query_definition: createEmptyQueryDefinition(),
      },
      undefined,
      { includeTechnical: false },
    );

    const url = requestedUrl(fetchMock);
    expect(url.searchParams.get("skip_technical")).toBe("true");
    expect(url.searchParams.get("library_id")).toBe("7");
    expect(url.searchParams.has("q")).toBe(false);
    expect(url.searchParams.has("sort")).toBe(false);
  });

  it("searches one facet by prefix", async () => {
    const fetchMock = stubFetch(searchCatalogFacetOk);

    const result = await fetchCatalogFacetSearch(
      { source: "query", query_definition: createEmptyQueryDefinition() },
      "author",
      "fra",
      10,
    );

    const url = requestedUrl(fetchMock);
    expect(url.pathname).toBe("/api/v2/catalog/filters/search");
    expect(url.searchParams.get("facet")).toBe("author");
    expect(url.searchParams.get("q")).toBe("fra");
    expect(url.searchParams.get("limit")).toBe("10");
    expect(result).toEqual({ matches: ["Frank Herbert"], has_more: true });
  });

  it("posts structured filters, query cap and explicit sorting without bracket parameters", async () => {
    const fetchMock = stubFetch({ ...queryCatalogItemsOk, window_cursor: "opaque-window" });
    const query = createEmptyQueryDefinition();
    query.match = "any";
    query.groups = [
      { match: "all", rules: [{ field: "genre", op: "in", value: ["Crime", "Drama"] }] },
    ];
    query.limit = 250;
    query.sort = { field: "title", order: "asc" };
    query.library_ids = [7];
    const signal = new AbortController().signal;

    const result = await fetchCatalogPage({ source: "query", query_definition: query }, 60, 0, {
      signal,
    });

    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/catalog/query");
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ method: "POST", signal });
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      source: "query",
      library_id: "7",
      match: "any",
      groups: query.groups,
      sort: "title",
      order: "asc",
      query_limit: 250,
      limit: 60,
    });
    expect(result.snapshot).toBe("opaque-window");
    expect(result.items[0]?.poster_url).toBe("");
    expect(result.items[0]?.rating_imdb).toBeNull();
  });

  it("jumps directly using the opaque seed and skips the total", async () => {
    const fetchMock = stubFetch({ ...queryCatalogItemsOk, window_cursor: "opaque-window" });
    await fetchCatalogPage(
      { source: "query", query_definition: createEmptyQueryDefinition() },
      60,
      60000,
      undefined,
      false,
      "opaque-window",
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toMatchObject({
      cursor: "opaque-window",
      seek: 60000,
      skip_total: true,
    });
  });

  it("seeks back to zero when reusing a snapshot", async () => {
    const fetchMock = stubFetch(queryCatalogItemsOk);
    await fetchCatalogPage(
      {
        source: "favorites",
        query_definition: createEmptyQueryDefinition(),
        uses_source_order: true,
      },
      60,
      0,
      undefined,
      false,
      "opaque-window",
    );
    const body = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body));
    expect(body).toMatchObject({ cursor: "opaque-window", seek: 0 });
    expect(body).not.toHaveProperty("sort");
    expect(body).not.toHaveProperty("order");
  });

  it("does not overlay a section's saved filters or order", async () => {
    const fetchMock = stubFetch(queryCatalogItemsOk);
    const query = createEmptyQueryDefinition();
    query.groups = [{ match: "all", rules: [{ field: "year", op: "gte", value: 2000 }] }];
    await fetchCatalogPage(
      {
        source: "section",
        scope: "library",
        library_id: 7,
        section_id: "recent",
        q: "ignored",
        query_definition: query,
      },
      60,
      0,
    );
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      source: "section",
      scope: "library",
      library_id: "7",
      section_id: "recent",
      limit: 60,
    });
  });

  it("preserves provider search diagnostics", async () => {
    const diagnostics = {
      provider: "postgres",
      mode: "keyword",
      semantic_used: false,
      fallback_reason: "semantic_unavailable",
      index_pending_updates: 4,
    };
    stubFetch({
      ...queryCatalogItemsOk,
      window_cursor: "opaque-window",
      search_diagnostics: diagnostics,
    });
    const result = await fetchCatalogPage(
      { source: "query", q: "Dune", query_definition: createEmptyQueryDefinition() },
      60,
      0,
    );
    expect(result.search_diagnostics).toEqual(diagnostics);
  });

  it("rejects seeks outside the server bound before dispatch", async () => {
    const fetchMock = stubFetch(queryCatalogItemsOk);
    await expect(
      fetchCatalogPage(
        { source: "query", query_definition: createEmptyQueryDefinition() },
        60,
        10_000_001,
      ),
    ).rejects.toBeInstanceOf(RangeError);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
