// @vitest-environment node

import { describe, expect, it } from "vitest";

import {
  applySavedLibraryPageSearchParams,
  hasLibraryPageSearchParams,
  parseLibraryPageState,
  serializeLibraryPageSearchParams,
  updateLibraryPageSearchParams,
} from "./libraryPageSearchParams";

function params(search: string) {
  return new URLSearchParams(search);
}

function asObject(searchParams: URLSearchParams) {
  return Object.fromEntries(searchParams.entries());
}

describe("parseLibraryPageState", () => {
  it("uses recommended tab and title ascending defaults when params are absent", () => {
    const state = parseLibraryPageState(params(""), "mixed");

    expect(state).toEqual({
      activeTab: "recommended",
      browseType: "series",
      queryDefinition: {
        library_ids: [],
        match: "all",
        groups: [],
        sort: { field: "title", order: "asc" },
      },
    });
  });

  it("ignores library filters unless tab=library is set", () => {
    const state = parseLibraryPageState(
      params("genre=Crime&sort=year&order=desc&year_min=1990"),
      "mixed",
    );

    expect(state).toEqual({
      activeTab: "recommended",
      browseType: "series",
      queryDefinition: {
        library_ids: [],
        match: "all",
        groups: [],
        sort: { field: "title", order: "asc" },
      },
    });
  });

  it("treats tab=collections as a non-browse tab with default filters", () => {
    const state = parseLibraryPageState(params("tab=collections&genre=Crime&sort=year"), "mixed");

    expect(state).toEqual({
      activeTab: "collections",
      browseType: "series",
      queryDefinition: {
        library_ids: [],
        match: "all",
        groups: [],
        sort: { field: "title", order: "asc" },
      },
    });
  });

  it("sanitizes invalid values and ignores type filters for non-mixed libraries", () => {
    const state = parseLibraryPageState(
      params(
        "tab=library&type=series&sort=nope&order=sideways&genre=Unknown&year_min=abc&year_max=2005&content_rating=wat",
      ),
      "movies",
    );

    expect(state.activeTab).toBe("library");
    expect(state.queryDefinition.media_scope).toBeUndefined();
    expect(state.queryDefinition.sort).toEqual({ field: "title", order: "asc" });
    expect(state.queryDefinition.groups).toEqual([
      {
        match: "all",
        rules: [{ field: "genre", op: "contains", value: "Unknown" }],
      },
      {
        match: "all",
        rules: [{ field: "year", op: "lte", value: 2005 }],
      },
      {
        match: "all",
        rules: [{ field: "content_rating", op: "is", value: "wat" }],
      },
    ]);
  });

  it("does not honor type filters until the library type is known", () => {
    const state = parseLibraryPageState(params("tab=library&type=movie&genre=Crime"), "");

    expect(state.activeTab).toBe("library");
    expect(state.queryDefinition.media_scope).toBeUndefined();
    expect(state.queryDefinition.groups).toEqual([
      {
        match: "all",
        rules: [{ field: "genre", op: "contains", value: "Crime" }],
      },
    ]);
  });

  it("parses valid legacy library filters for mixed libraries", () => {
    const state = parseLibraryPageState(
      params(
        "tab=library&type=movie&sort=year&order=desc&genre=Crime&year_min=1970&year_max=2010&content_rating=R",
      ),
      "mixed",
    );

    expect(state.activeTab).toBe("library");
    expect(state.queryDefinition.media_scope).toBe("movie");
    expect(state.queryDefinition.sort).toEqual({ field: "year", order: "desc" });
    expect(state.queryDefinition.groups).toEqual([
      {
        match: "all",
        rules: [{ field: "genre", op: "contains", value: "Crime" }],
      },
      {
        match: "all",
        rules: [{ field: "year", op: "between", value: [1970, 2010] }],
      },
      {
        match: "all",
        rules: [{ field: "content_rating", op: "is", value: "R" }],
      },
    ]);
  });

  it("preserves audiobook scope for mixed library filters", () => {
    const state = parseLibraryPageState(params("tab=library&type=audiobook&sort=author"), "mixed");

    expect(state.activeTab).toBe("library");
    expect(state.queryDefinition.media_scope).toBe("audiobook");
    expect(state.queryDefinition.sort).toEqual({ field: "author", order: "asc" });
  });

  it("preserves ebook scope for mixed library filters", () => {
    const state = parseLibraryPageState(params("tab=library&type=ebook&sort=author"), "mixed");

    expect(state.activeTab).toBe("library");
    expect(state.queryDefinition.media_scope).toBe("ebook");
    expect(state.queryDefinition.sort).toEqual({ field: "author", order: "asc" });
  });

  it("accepts grouped query params and canonical sorts", () => {
    const state = parseLibraryPageState(
      params(
        "tab=library&sort=release_date&order=desc&groups[0][match]=all&groups[0][rules][0][field]=actor&groups[0][rules][0][op]=is&groups[0][rules][0][value]=Tom%20Hanks",
      ),
      "mixed",
    );

    expect(state.queryDefinition.sort).toEqual({ field: "release_date", order: "desc" });
    expect(state.queryDefinition.groups).toEqual([
      {
        match: "all",
        rules: [{ field: "actor", op: "is", value: "Tom Hanks" }],
      },
    ]);
  });

  it("accepts last_air_date as a valid sort value", () => {
    const state = parseLibraryPageState(
      params("tab=library&sort=last_air_date&order=desc"),
      "series",
    );

    expect(state.queryDefinition.sort).toEqual({ field: "last_air_date", order: "desc" });
  });

  it("parses series-library browse mode from the type param", () => {
    const state = parseLibraryPageState(params("tab=library&type=episode"), "series");

    expect(state.browseType).toBe("episode");
    expect(state.queryDefinition.sort).toEqual({ field: "title", order: "asc" });
  });

  it("normalizes series-only sorts away in episode browse mode", () => {
    const state = parseLibraryPageState(
      params("tab=library&type=episode&sort=last_air_date&order=desc"),
      "series",
    );

    expect(state.browseType).toBe("episode");
    expect(state.queryDefinition.sort).toEqual({ field: "title", order: "asc" });
  });

  it("normalizes video-only sorts away on audiobook libraries", () => {
    const state = parseLibraryPageState(
      params("tab=library&sort=rating_imdb&order=desc"),
      "audiobooks",
    );
    expect(state.queryDefinition.media_scope).toBe("audiobook");
    expect(state.queryDefinition.sort).toEqual({ field: "title", order: "asc" });
  });

  it("keeps audiobook-applicable sorts on audiobook libraries", () => {
    const state = parseLibraryPageState(
      params("tab=library&sort=runtime&order=desc"),
      "audiobooks",
    );
    expect(state.queryDefinition.media_scope).toBe("audiobook");
    expect(state.queryDefinition.sort).toEqual({ field: "runtime", order: "desc" });
  });

  it("uses ebook scope for ebook libraries", () => {
    const state = parseLibraryPageState(params("tab=library&sort=author&order=asc"), "ebooks");

    expect(state.queryDefinition.media_scope).toBe("ebook");
    expect(state.queryDefinition.sort).toEqual({ field: "author", order: "asc" });
  });

  it("normalizes legacy sort aliases to canonical values", () => {
    expect(
      parseLibraryPageState(params("tab=library&sort=sort_title"), "mixed").queryDefinition.sort
        .field,
    ).toBe("title");
    expect(
      parseLibraryPageState(params("tab=library&sort=recently_added"), "mixed").queryDefinition.sort
        .field,
    ).toBe("added_at");
    expect(
      parseLibraryPageState(params("tab=library&sort=rating"), "mixed").queryDefinition.sort.field,
    ).toBe("rating_imdb");
  });
});

describe("updateLibraryPageSearchParams", () => {
  it("writes grouped query params and preserves unrelated params", () => {
    const next = updateLibraryPageSearchParams(
      params("foo=bar"),
      {
        activeTab: "library",
        browseType: "series",
        queryDefinition: {
          library_ids: [],
          media_scope: "movie",
          match: "all",
          groups: [
            {
              match: "all",
              rules: [{ field: "genre", op: "contains", value: "Crime" }],
            },
          ],
          sort: { field: "title", order: "asc" },
        },
      },
      "mixed",
    );

    expect(asObject(next)).toEqual({
      foo: "bar",
      tab: "library",
      type: "movie",
      "groups[0][match]": "all",
      "groups[0][rules][0][field]": "genre",
      "groups[0][rules][0][op]": "contains",
      "groups[0][rules][0][value]": "Crime",
    });
  });

  it("omits default sort params and legacy filter params from canonical writes", () => {
    const next = updateLibraryPageSearchParams(
      params("tab=library&genre=Crime&year_min=2000"),
      {
        activeTab: "library",
        browseType: "series",
        queryDefinition: {
          library_ids: [],
          match: "all",
          groups: [],
          sort: { field: "title", order: "asc" },
        },
      },
      "mixed",
    );

    expect(asObject(next)).toEqual({ tab: "library" });
  });

  it("removes library params when switching back to recommended", () => {
    const next = updateLibraryPageSearchParams(
      params("foo=bar&tab=library&type=movie&groups[0][match]=all"),
      {
        activeTab: "recommended",
        browseType: "series",
        queryDefinition: {
          library_ids: [],
          match: "all",
          groups: [],
          sort: { field: "title", order: "asc" },
        },
      },
      "mixed",
    );

    expect(asObject(next)).toEqual({ foo: "bar" });
  });

  it("writes only the collections tab marker and drops browse-only filters", () => {
    const next = updateLibraryPageSearchParams(
      params("foo=bar&tab=library&type=movie&groups[0][match]=all"),
      {
        activeTab: "collections",
        browseType: "series",
        queryDefinition: {
          library_ids: [],
          match: "all",
          groups: [],
          sort: { field: "title", order: "asc" },
        },
      },
      "mixed",
    );

    expect(asObject(next)).toEqual({
      foo: "bar",
      tab: "collections",
    });
  });

  it("writes the episode browse mode for series libraries", () => {
    const next = updateLibraryPageSearchParams(
      params("foo=bar"),
      {
        activeTab: "library",
        browseType: "episode",
        queryDefinition: {
          library_ids: [],
          match: "all",
          groups: [],
          sort: { field: "last_air_date", order: "desc" },
        },
      },
      "series",
    );

    expect(asObject(next)).toEqual({
      foo: "bar",
      tab: "library",
      type: "episode",
    });
  });

  it("does not write a redundant type param for audiobook libraries", () => {
    const next = updateLibraryPageSearchParams(
      params("foo=bar"),
      {
        activeTab: "library",
        browseType: "series",
        queryDefinition: {
          library_ids: [],
          media_scope: "audiobook",
          match: "all",
          groups: [],
          sort: { field: "author", order: "asc" },
        },
      },
      "audiobooks",
    );

    expect(asObject(next)).toEqual({
      foo: "bar",
      tab: "library",
      sort: "author",
      order: "asc",
    });
  });
});

describe("library page saved state helpers", () => {
  it("detects explicit library state params", () => {
    expect(hasLibraryPageSearchParams(params(""))).toBe(false);
    expect(hasLibraryPageSearchParams(params("foo=bar"))).toBe(false);
    expect(hasLibraryPageSearchParams(params("tab=collections"))).toBe(true);
    expect(hasLibraryPageSearchParams(params("sort=year"))).toBe(true);
    expect(hasLibraryPageSearchParams(params("groups[0][rules][0][field]=genre"))).toBe(true);
  });

  it("serializes only library state params", () => {
    const serialized = serializeLibraryPageSearchParams(
      params(
        "foo=bar&tab=library&sort=year&order=desc&groups[0][match]=all&groups[0][rules][0][field]=genre",
      ),
    );

    expect(Object.fromEntries(new URLSearchParams(serialized).entries())).toEqual({
      tab: "library",
      sort: "year",
      order: "desc",
      "groups[0][match]": "all",
      "groups[0][rules][0][field]": "genre",
    });
  });

  it("applies saved library state while preserving unrelated params", () => {
    const next = applySavedLibraryPageSearchParams(
      params("foo=bar"),
      "tab=library&sort=year&order=desc",
    );

    expect(asObject(next)).toEqual({
      foo: "bar",
      tab: "library",
      sort: "year",
      order: "desc",
    });
  });
});
