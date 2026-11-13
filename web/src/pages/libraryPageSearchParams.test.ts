// @vitest-environment node

import { describe, expect, it } from "vitest";

import {
  applySavedLibraryPageSearchParams,
  getLibrarySortRelevanceScope,
  hasLibraryPageSearchParams,
  parseLibraryPageState,
  serializeLibraryPageSearchParams,
  updateLibraryPageSearchParams,
  type LibraryPageState,
} from "./libraryPageSearchParams";

function params(search: string) {
  return new URLSearchParams(search);
}

function asObject(searchParams: URLSearchParams) {
  return Object.fromEntries(searchParams.entries());
}

const DEFAULT_QUERY = {
  library_ids: [],
  match: "all",
  groups: [],
  sort: { field: "title", order: "asc" },
} satisfies LibraryPageState["queryDefinition"];

function pageState(
  activeTab: LibraryPageState["activeTab"],
  browseType: LibraryPageState["browseType"] = "series",
  query: Partial<LibraryPageState["queryDefinition"]> = {},
): LibraryPageState {
  return { activeTab, browseType, queryDefinition: { ...DEFAULT_QUERY, ...query } };
}

describe("parseLibraryPageState", () => {
  it.each<[string, string, LibraryPageState["activeTab"]]>([
    ["uses recommended tab and title ascending defaults when params are absent", "", "recommended"],
    [
      "ignores library filters unless tab=library is set",
      "genre=Crime&sort=year&order=desc&year_min=1990",
      "recommended",
    ],
    [
      "treats tab=collections as a non-browse tab with default filters",
      "tab=collections&genre=Crime&sort=year",
      "collections",
    ],
  ])("%s", (_name, search, activeTab) => {
    expect(parseLibraryPageState(params(search), "mixed")).toEqual({
      activeTab,
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

  it.each([
    ["parses series-library browse mode from the type param", "tab=library&type=episode"],
    [
      "normalizes series-only sorts away in episode browse mode",
      "tab=library&type=episode&sort=last_air_date&order=desc",
    ],
  ])("%s", (_name, search) => {
    const state = parseLibraryPageState(params(search), "series");

    expect(state.browseType).toBe("episode");
    expect(state.queryDefinition.sort).toEqual({ field: "title", order: "asc" });
  });

  it.each([
    [
      "normalizes video-only sorts away on audiobook libraries",
      "tab=library&sort=rating_imdb&order=desc",
      "audiobooks",
      "audiobook",
      { field: "title", order: "asc" },
    ],
    [
      "keeps audiobook-applicable sorts on audiobook libraries",
      "tab=library&sort=runtime&order=desc",
      "audiobooks",
      "audiobook",
      { field: "runtime", order: "desc" },
    ],
    // Manga sort relevance mirrors ebooks, so ebook-applicable sorts survive.
    [
      "uses manga scope for manga libraries",
      "tab=library&sort=author&order=asc",
      "manga",
      "manga",
      { field: "author", order: "asc" },
    ],
  ])("%s", (_name, search, libraryType, scope, sort) => {
    const state = parseLibraryPageState(params(search), libraryType);
    expect(state.queryDefinition.media_scope).toBe(scope);
    expect(state.queryDefinition.sort).toEqual(sort);
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
  it.each<[string, string, LibraryPageState, string, Record<string, string>]>([
    [
      "writes grouped query params and preserves unrelated params",
      "foo=bar",
      pageState("library", "series", {
        media_scope: "movie",
        groups: [{ match: "all", rules: [{ field: "genre", op: "contains", value: "Crime" }] }],
      }),
      "mixed",
      {
        foo: "bar",
        tab: "library",
        type: "movie",
        "groups[0][match]": "all",
        "groups[0][rules][0][field]": "genre",
        "groups[0][rules][0][op]": "contains",
        "groups[0][rules][0][value]": "Crime",
      },
    ],
    [
      "omits default sort params and legacy filter params from canonical writes",
      "tab=library&genre=Crime&year_min=2000",
      pageState("library"),
      "mixed",
      { tab: "library" },
    ],
    [
      "removes library params when switching back to recommended",
      "foo=bar&tab=library&type=movie&groups[0][match]=all",
      pageState("recommended"),
      "mixed",
      { foo: "bar" },
    ],
    [
      "writes only the collections tab marker and drops browse-only filters",
      "foo=bar&tab=library&type=movie&groups[0][match]=all",
      pageState("collections"),
      "mixed",
      { foo: "bar", tab: "collections" },
    ],
    [
      "writes the episode browse mode for series libraries",
      "foo=bar",
      pageState("library", "episode", { sort: { field: "last_air_date", order: "desc" } }),
      "series",
      { foo: "bar", tab: "library", type: "episode" },
    ],
    [
      "does not write a redundant type param for audiobook libraries",
      "foo=bar",
      pageState("library", "books", {
        media_scope: "audiobook",
        sort: { field: "author", order: "asc" },
      }),
      "audiobooks",
      { foo: "bar", tab: "library", sort: "author", order: "asc" },
    ],
  ])("%s", (_name, search, state, libraryType, expected) => {
    expect(asObject(updateLibraryPageSearchParams(params(search), state, libraryType))).toEqual(
      expected,
    );
  });

  it("round-trips audiobook browse axes through the type param", () => {
    for (const axis of ["series", "authors", "narrators"] as const) {
      const next = updateLibraryPageSearchParams(
        params(""),
        pageState("library", axis, { media_scope: "audiobook" }),
        "audiobooks",
      );

      expect(next.get("type")).toBe(axis);
      expect(parseLibraryPageState(next, "audiobooks").browseType).toBe(axis);
    }
  });

  it("defaults audiobook libraries to the books axis for unknown type values", () => {
    const state = parseLibraryPageState(params("tab=library&type=episode"), "audiobooks");

    expect(state.browseType).toBe("books");
    expect(state.queryDefinition.media_scope).toBe("audiobook");
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

describe("getLibrarySortRelevanceScope", () => {
  it("falls back to the media scope and then to all for mixed libraries", () => {
    expect(getLibrarySortRelevanceScope("mixed", "episode")).toBe("episode");
    expect(getLibrarySortRelevanceScope("mixed", "ebook")).toBe("ebook");
    expect(getLibrarySortRelevanceScope("mixed")).toBe("all");
    expect(getLibrarySortRelevanceScope("unknown")).toBe("all");
  });
});
