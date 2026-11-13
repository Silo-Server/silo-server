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

vi.mock("@/hooks/queries/personSearch", () => ({
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

vi.mock("@/hooks/queries/ratingsCapability", () => ({
  useShownRatingSources: () => new Set(["imdb", "tmdb"]),
}));

describe("queryDefinitionToGuidedState", () => {
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

  it("round-trips ebook-native fields without narrator", () => {
    const original: QueryDefinition = {
      library_ids: [10],
      media_scope: "ebook",
      match: "all",
      groups: [
        {
          match: "all",
          rules: [
            { field: "author", op: "is", value: "Ursula K. Le Guin" },
            { field: "narrator", op: "is", value: "Should Not Apply" },
            { field: "series", op: "is", value: "Earthsea" },
          ],
        },
      ],
      sort: { field: "author", order: "asc" },
    };

    const state = queryDefinitionToGuidedState(original);
    expect(state.author).toBe("Ursula K. Le Guin");
    expect(state.narrator).toBe("");
    expect(state.series).toBe("Earthsea");

    const rebuilt = guidedStateToQueryDefinition(
      {
        ...state,
        narrator: "Should Not Serialize",
      },
      original,
    );
    const rules = rebuilt.groups[0]!.rules;
    expect(rules).toContainEqual({ field: "author", op: "is", value: "Ursula K. Le Guin" });
    expect(rules).toContainEqual({ field: "series", op: "is", value: "Earthsea" });
    expect(rules).not.toContainEqual({
      field: "narrator",
      op: "is",
      value: "Should Not Serialize",
    });
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
  it("renders ebook book-native filters without audiobook narrator or video-only filters", () => {
    const markup = renderToStaticMarkup(
      <CollectionGuidedRulesEditor
        value={{
          ...createEmptyQueryDefinition(),
          media_scope: "ebook",
        }}
        onChange={() => {}}
        libraryType="ebooks"
      />,
    );

    expect(markup).toContain("Author");
    expect(markup).toContain("Series");
    expect(markup).not.toContain("Narrator");
    expect(markup).not.toContain("Actor");
    expect(markup).not.toContain("Director");
    expect(markup).not.toContain("Minimum IMDb Rating");
    expect(markup).not.toContain("Video Quality");
  });
});
