import { fireEvent, render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import type { QueryDefinition, QueryDefinitionInput } from "@/api/types";
import { createEmptyQueryDefinition } from "@/api/types";

vi.mock("@/hooks/queries/catalog", () => ({
  useCatalogMetadataFilters: () => ({
    data: {
      genres: [],
      studios: [],
      networks: [],
      countries: [],
      content_ratings: [],
      original_languages: ["en", "ja"],
    },
    isLoading: false,
  }),
}));

vi.mock("@/hooks/queries/people", () => ({
  usePersonSearch: () => ({
    data: [],
    isLoading: false,
  }),
}));

import {
  guidedStateToQueryDefinition,
  queryDefinitionToGuidedState,
  type GuidedFormState,
} from "./CollectionGuidedRulesEditor";
import CollectionGuidedRulesEditor from "./CollectionGuidedRulesEditor";

function emptyState(): GuidedFormState {
  return {
    mediaScope: "all",
    libraryIds: [],
    genres: [],
    decade: "",
    yearFrom: "",
    yearTo: "",
    minRating: "",
    contentRating: "",
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
    originalLanguages: [],
    fourK: false,
    hdr: false,
    dolbyVision: false,
    sortField: "added_at",
    sortOrder: "desc",
  };
}

describe("queryDefinitionToGuidedState", () => {
  it("returns defaults for an empty query definition", () => {
    const state = queryDefinitionToGuidedState(createEmptyQueryDefinition());
    expect(state).toEqual(emptyState());
  });

  it("extracts fields from a populated query definition", () => {
    const qd: QueryDefinition = {
      library_ids: [1, 2],
      media_scope: "movie",
      match: "all",
      groups: [
        {
          match: "all",
          rules: [
            { field: "genre", op: "is", value: "Action" },
            { field: "genre", op: "is", value: "Thriller" },
            { field: "year", op: "gte", value: 2020 },
            { field: "year", op: "lte", value: 2025 },
            { field: "rating_imdb", op: "gte", value: 7.5 },
            { field: "content_rating", op: "is", value: "R" },
            { field: "actor", op: "is", value: "Sigourney Weaver" },
            { field: "director", op: "is", value: "Greta Gerwig" },
            { field: "studio", op: "is", value: "A24" },
            { field: "writer", op: "is", value: "Phoebe Waller-Bridge" },
            { field: "producer", op: "is", value: "Kevin Feige" },
            { field: "original_language", op: "is", value: "English" },
            { field: "status", op: "is", value: "matched" },
            { field: "watched", op: "is", value: true },
            { field: "resolution", op: "is", value: "4k" },
            { field: "hdr", op: "is", value: true },
            { field: "dolby_vision", op: "is", value: true },
            { field: "added_at", op: "in_last", value: "30d" },
          ],
        },
      ],
      sort: { field: "release_date", order: "asc" },
    };

    const state = queryDefinitionToGuidedState(qd);
    expect(state.mediaScope).toBe("movie");
    expect(state.libraryIds).toEqual([1, 2]);
    expect(state.genres).toEqual(["Action", "Thriller"]);
    expect(state.yearFrom).toBe("2020");
    expect(state.yearTo).toBe("2025");
    expect(state.minRating).toBe("7.5");
    expect(state.contentRating).toBe("R");
    expect(state.actor).toBe("Sigourney Weaver");
    expect(state.director).toBe("Greta Gerwig");
    expect(state.studio).toBe("A24");
    expect(state.writer).toBe("Phoebe Waller-Bridge");
    expect(state.producer).toBe("Kevin Feige");
    expect(state.originalLanguages).toEqual(["English"]);
    expect(state.status).toBe("matched");
    expect(state.watchStatus).toBe("watched");
    expect(state.fourK).toBe(true);
    expect(state.hdr).toBe(true);
    expect(state.dolbyVision).toBe(true);
    expect(state.addedInLast).toBe("30d");
    expect(state.sortField).toBe("release_date");
    expect(state.sortOrder).toBe("asc");
  });
});

describe("guidedStateToQueryDefinition", () => {
  const base = createEmptyQueryDefinition();

  it("produces an empty groups array for an empty state", () => {
    const qd = guidedStateToQueryDefinition(emptyState(), base);
    expect(qd.groups).toEqual([]);
    expect(qd.match).toBe("all");
    expect(qd.media_scope).toBeUndefined();
  });

  it("builds rules from a populated state", () => {
    const state: GuidedFormState = {
      mediaScope: "series",
      libraryIds: [3],
      genres: ["Drama", "Sci-Fi"],
      decade: "",
      yearFrom: "2015",
      yearTo: "",
      minRating: "8",
      contentRating: "",
      actor: "Carrie-Anne Moss",
      director: "Denis Villeneuve",
      writer: "Tony Gilroy",
      producer: "Emma Thomas",
      author: "",
      narrator: "",
      series: "",
      studio: "",
      originalLanguages: ["Japanese"],
      network: "HBO",
      country: "US",
      status: "matched",
      watchStatus: "unwatched",
      addedInLast: "",
      releasedInLast: "1y",
      fourK: true,
      hdr: true,
      dolbyVision: false,
      sortField: "rating_imdb",
      sortOrder: "desc",
    };

    const qd = guidedStateToQueryDefinition(state, base);
    expect(qd.media_scope).toBe("series");
    expect(qd.library_ids).toEqual([3]);
    expect(qd.sort).toEqual({ field: "rating_imdb", order: "desc" });
    expect(qd.groups).toHaveLength(1);

    const rules = qd.groups[0]!.rules;
    expect(rules).toContainEqual({ field: "genre", op: "is", value: "Drama" });
    expect(rules).toContainEqual({ field: "genre", op: "is", value: "Sci-Fi" });
    expect(rules).toContainEqual({ field: "year", op: "gte", value: 2015 });
    expect(rules).toContainEqual({ field: "rating_imdb", op: "gte", value: 8 });
    expect(rules).toContainEqual({ field: "actor", op: "is", value: "Carrie-Anne Moss" });
    expect(rules).toContainEqual({ field: "director", op: "is", value: "Denis Villeneuve" });
    expect(rules).toContainEqual({ field: "writer", op: "is", value: "Tony Gilroy" });
    expect(rules).toContainEqual({ field: "producer", op: "is", value: "Emma Thomas" });
    expect(rules).toContainEqual({ field: "network", op: "is", value: "HBO" });
    expect(rules).toContainEqual({ field: "country", op: "is", value: "US" });
    expect(rules).toContainEqual({ field: "original_language", op: "is", value: "Japanese" });
    expect(rules).toContainEqual({ field: "status", op: "is", value: "matched" });
    expect(rules).toContainEqual({ field: "watched", op: "is", value: false });
    expect(rules).toContainEqual({ field: "in_progress", op: "is", value: false });
    expect(rules).toContainEqual({ field: "resolution", op: "is", value: "2160p" });
    expect(rules).toContainEqual({ field: "hdr", op: "is", value: true });
    expect(rules).toContainEqual({ field: "release_date", op: "in_last", value: "1y" });
  });

  it("round-trips through parse and serialize", () => {
    const original: QueryDefinition = {
      library_ids: [1],
      media_scope: "movie",
      match: "all",
      groups: [
        {
          match: "all",
          rules: [
            { field: "genre", op: "is", value: "Comedy" },
            { field: "year", op: "gte", value: 2000 },
            { field: "year", op: "lte", value: 2020 },
            { field: "rating_imdb", op: "gte", value: 6 },
            { field: "original_language", op: "is", value: "French" },
          ],
        },
      ],
      sort: { field: "title", order: "asc" },
    };

    const state = queryDefinitionToGuidedState(original);
    const rebuilt = guidedStateToQueryDefinition(state, original);

    expect(rebuilt.library_ids).toEqual([1]);
    expect(rebuilt.media_scope).toBe("movie");
    expect(rebuilt.sort).toEqual({ field: "title", order: "asc" });

    const rules = rebuilt.groups[0]!.rules;
    expect(rules).toContainEqual({ field: "genre", op: "is", value: "Comedy" });
    expect(rules).toContainEqual({ field: "year", op: "gte", value: 2000 });
    expect(rules).toContainEqual({ field: "year", op: "lte", value: 2020 });
    expect(rules).toContainEqual({ field: "rating_imdb", op: "gte", value: 6 });
    expect(rules).toContainEqual({ field: "original_language", op: "is", value: "French" });
  });

  it("collects multiple original_language rules and emits them as an OR group", () => {
    const original: QueryDefinition = {
      library_ids: [],
      media_scope: "movie",
      match: "all",
      groups: [
        { match: "all", rules: [{ field: "genre", op: "is", value: "Drama" }] },
        {
          match: "any",
          rules: [
            { field: "original_language", op: "is", value: "en" },
            { field: "original_language", op: "is", value: "fr" },
          ],
        },
      ],
      sort: { field: "added_at", order: "desc" },
    };

    const state = queryDefinitionToGuidedState(original);
    expect(state.originalLanguages).toEqual(["en", "fr"]);

    const rebuilt = guidedStateToQueryDefinition(state, original);
    expect(rebuilt.groups).toHaveLength(2);
    const [first, second] = rebuilt.groups;
    expect(first!.match).toBe("all");
    expect(first!.rules).toContainEqual({ field: "genre", op: "is", value: "Drama" });
    expect(second!.match).toBe("any");
    expect(second!.rules).toEqual([
      { field: "original_language", op: "is", value: "en" },
      { field: "original_language", op: "is", value: "fr" },
    ]);
  });

  it("round-trips audiobook-native fields (author/narrator/series)", () => {
    const original: QueryDefinition = {
      library_ids: [9],
      media_scope: "audiobook",
      match: "all",
      groups: [
        {
          match: "all",
          rules: [
            { field: "author", op: "is", value: "Brandon Sanderson" },
            { field: "narrator", op: "is", value: "Michael Kramer" },
            { field: "series", op: "is", value: "Mistborn" },
          ],
        },
      ],
      sort: { field: "title", order: "asc" },
    };

    const state = queryDefinitionToGuidedState(original);
    expect(state.author).toBe("Brandon Sanderson");
    expect(state.narrator).toBe("Michael Kramer");
    expect(state.series).toBe("Mistborn");

    const rebuilt = guidedStateToQueryDefinition(state, original);
    const rules = rebuilt.groups[0]!.rules;
    expect(rules).toContainEqual({ field: "author", op: "is", value: "Brandon Sanderson" });
    expect(rules).toContainEqual({ field: "narrator", op: "is", value: "Michael Kramer" });
    expect(rules).toContainEqual({ field: "series", op: "is", value: "Mistborn" });
  });

  it("omits narrator rules for ebook scope", () => {
    const state: GuidedFormState = {
      ...emptyState(),
      mediaScope: "ebook",
      author: "Octavia Butler",
      narrator: "Should Not Persist",
      series: "Patternist",
    };

    const rebuilt = guidedStateToQueryDefinition(state, createEmptyQueryDefinition());
    const rules = rebuilt.groups[0]!.rules;
    expect(rebuilt.media_scope).toBe("ebook");
    expect(rules).toContainEqual({ field: "author", op: "is", value: "Octavia Butler" });
    expect(rules).toContainEqual({ field: "series", op: "is", value: "Patternist" });
    expect(rules).not.toContainEqual({
      field: "narrator",
      op: "is",
      value: "Should Not Persist",
    });
  });

  it("round-trips last_air_date sort through parse and serialize", () => {
    const original: QueryDefinition = {
      library_ids: [1],
      media_scope: "series",
      match: "all",
      groups: [],
      sort: { field: "last_air_date", order: "desc" },
    };

    const state = queryDefinitionToGuidedState(original);
    const rebuilt = guidedStateToQueryDefinition(state, original);

    expect(state.sortField).toBe("last_air_date");
    expect(rebuilt.sort).toEqual({ field: "last_air_date", order: "desc" });
  });

  it("accepts legacy rating aliases when parsing and emits canonical rating_imdb rules", () => {
    const original: QueryDefinitionInput = {
      library_ids: [],
      match: "all",
      groups: [
        {
          match: "all",
          rules: [{ field: "rating", op: "gte", value: 7.2 }],
        },
      ],
      sort: { field: "rating", order: "desc" },
    };

    const state = queryDefinitionToGuidedState(original);
    const rebuilt = guidedStateToQueryDefinition(state, original);

    expect(state.minRating).toBe("7.2");
    expect(state.sortField).toBe("rating_imdb");
    expect(rebuilt.sort).toEqual({ field: "rating_imdb", order: "desc" });
    expect(rebuilt.groups[0]!.rules).toContainEqual({
      field: "rating_imdb",
      op: "gte",
      value: 7.2,
    });
  });
});

describe("CollectionGuidedRulesEditor original language field", () => {
  it("renders the original language select with the current value", () => {
    const markup = renderToStaticMarkup(
      <CollectionGuidedRulesEditor
        value={{
          ...createEmptyQueryDefinition(),
          groups: [
            {
              match: "all",
              rules: [{ field: "original_language", op: "is", value: "English" }],
            },
          ],
        }}
        onChange={() => {}}
      />,
    );

    expect(markup).toContain("Original Language");
    expect(markup).toContain("English");
  });

  it("renders the media type control", () => {
    const markup = renderToStaticMarkup(
      <CollectionGuidedRulesEditor value={createEmptyQueryDefinition()} onChange={() => {}} />,
    );

    expect(markup).toContain("Media Type");
  });

  it("renders ebook book facets without narrator or video-only controls", () => {
    const markup = renderToStaticMarkup(
      <CollectionGuidedRulesEditor
        value={{
          ...createEmptyQueryDefinition(),
          media_scope: "ebook",
        }}
        onChange={() => {}}
        libraryType="ebooks"
        filters={{ genres: [], studios: [], networks: [], countries: [], content_ratings: [] }}
      />,
    );

    expect(markup).toContain("Author");
    expect(markup).toContain("Series");
    expect(markup).not.toContain("Narrator");
    expect(markup).not.toContain("Minimum IMDb Rating");
    expect(markup).not.toContain("Video Quality");
  });

  it("does not render narrator for ebook libraries with stale audiobook scope", () => {
    const markup = renderToStaticMarkup(
      <CollectionGuidedRulesEditor
        value={{
          ...createEmptyQueryDefinition(),
          media_scope: "audiobook",
        }}
        onChange={() => {}}
        libraryType="ebooks"
        filters={{ genres: [], studios: [], networks: [], countries: [], content_ratings: [] }}
      />,
    );

    expect(markup).toContain("Author");
    expect(markup).toContain("Series");
    expect(markup).not.toContain("Narrator");
  });

  it("drops hidden stale narrator rules on ebook library edits", () => {
    let nextValue: QueryDefinition | undefined;

    render(
      <CollectionGuidedRulesEditor
        value={{
          ...createEmptyQueryDefinition(),
          groups: [
            {
              match: "all",
              rules: [
                { field: "author", op: "is", value: "Octavia Butler" },
                { field: "narrator", op: "is", value: "Should Not Persist" },
              ],
            },
          ],
        }}
        onChange={(value) => {
          nextValue = value;
        }}
        libraryType="ebooks"
        filters={{ genres: [], studios: [], networks: [], countries: [], content_ratings: [] }}
      />,
    );

    fireEvent.change(screen.getByPlaceholderText("e.g. 2000"), { target: { value: "1979" } });

    const rules = nextValue?.groups.flatMap((group) => group.rules) ?? [];
    expect(rules).toContainEqual({ field: "author", op: "is", value: "Octavia Butler" });
    expect(rules).toContainEqual({ field: "year", op: "gte", value: 1979 });
    expect(rules).not.toContainEqual({
      field: "narrator",
      op: "is",
      value: "Should Not Persist",
    });
  });
});
