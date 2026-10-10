import { describe, expect, it } from "vitest";
import {
  collapsedRowText,
  collectionKind,
  describeRow,
  ruleSortSummary,
  rowSwitchLabel,
  titleCount,
} from "./describe";
import { rowKindSentence } from "./catalog";
import { everyPreset } from "./recipeCatalogFixture.test-support";
import type { HomeRow } from "./types";

function row(sectionType: string, config: Record<string, unknown> = {}): HomeRow {
  return {
    id: "r1",
    title: "Row",
    sectionType,
    config,
    itemLimit: 20,
    hero: false,
    shown: true,
    own: false,
    legacyTrakt: false,
  };
}

const home = { pageKind: "home" as const };
const library = { pageKind: "library" as const };

function text(parts: ReturnType<typeof describeRow>): string {
  return parts
    .map((part) =>
      typeof part === "string" ? part : "strong" in part ? part.strong : part.warning,
    )
    .join("");
}

describe("describeRow", () => {
  it("says personalized rows are picked for each viewer", () => {
    expect(text(describeRow(row("continue_watching"), home))).toBe(
      "What each viewer is partway through",
    );
    expect(text(describeRow(row("recommended_for_you"), home))).toBe(
      "Picked for each viewer from their own watch history",
    );
    expect(text(describeRow(row("next_up"), home))).toBe(
      "The next episode of every show each viewer follows",
    );
  });

  it("speaks to the viewer on their own Home Screen settings", () => {
    const own = { pageKind: "home" as const, surface: "profile" as const };
    expect(text(describeRow(row("continue_watching"), own))).toBe("What you're partway through");
    expect(text(describeRow(row("recommended_for_you"), own))).toBe(
      "Picked from your watch history",
    );
    expect(text(describeRow(row("favorites"), own))).toBe("Your favorites");
    // Rows that aren't personal read the same on both surfaces.
    expect(text(describeRow(row("critically_acclaimed"), own))).toBe(
      "Rated 8.0+ on TMDB with 500+ votes",
    );
  });

  it("states the TMDB rating, vote and watch rule a ready-made row needs", () => {
    const home = { pageKind: "home" as const, surface: "admin" as const };
    const gem = row("hidden_gems", { min_rating: 7.5, max_play_count: 2 });
    expect(text(describeRow(gem, home))).toBe(
      "Rated 7.5+ on TMDB with 100+ votes, and watched twice or less",
    );
    const forgotten = row("forgotten_favorites", { lookback_days: 365 });
    expect(text(describeRow(forgotten, home))).toBe(
      "Rated 7.0+ on TMDB with 100+ votes, and not watched in the past year",
    );
    expect(text(describeRow(row("short_watches"), home))).toBe(
      "Movies of 95 minutes or less, rated 6.0+ on TMDB with 100+ votes",
    );
    // Zero or negative settings fall back to the server's defaults.
    expect(text(describeRow(row("short_watches", { max_minutes: 0 }), home))).toBe(
      "Movies of 95 minutes or less, rated 6.0+ on TMDB with 100+ votes",
    );
    expect(text(describeRow(row("forgotten_favorites", { lookback_days: -5 }), home))).toBe(
      "Rated 7.0+ on TMDB with 100+ votes, and not watched in the past year",
    );
  });

  it("calls the viewer's own collection theirs", () => {
    const context = {
      pageKind: "home" as const,
      collection: () => ({ title: "Comfort Shows", kind: "Manual", yours: true }),
    };
    expect(text(describeRow(row("collection", { user_collection_id: "c1" }), context))).toBe(
      "Your Comfort Shows collection (Manual)",
    );
  });

  it("says what a renamed row was called instead of what it shows", () => {
    const renamed = { ...row("trending_on_server", { window: "7d" }), renamedFrom: "Trending" };
    expect(describeRow(renamed, home)).toEqual(["Renamed from ", { strong: "Trending" }]);
  });

  it("follows the preset params of multi-preset rows", () => {
    expect(text(describeRow(row("trending_on_server", { window: "7d" }), home))).toBe(
      "Most played on this server in the last 7 days",
    );
    expect(text(describeRow(row("trending_on_server", { window: "24h" }), library))).toBe(
      "Most played here in the last 24 hours",
    );
    expect(text(describeRow(row("trending_discover", { window: "week" }), home))).toBe(
      "Trending on TMDB this week, from what you have",
    );
    expect(text(describeRow(row("format_showcase", { format: "4k", sort: "recent" }), home))).toBe(
      "The latest 4K additions",
    );
    expect(text(describeRow(row("continue_watching", { continue_type: "listening" }), home))).toBe(
      "Audiobooks each viewer is partway through",
    );
    expect(text(describeRow(row("editorial_spotlight", { subject_type: "director" }), home))).toBe(
      "A different director every week",
    );
    expect(
      text(
        describeRow(row("editorial_spotlight", { subject_type: "era", subject: "1980s" }), home),
      ),
    ).toBe("Titles from the 1980s");
  });

  it("says where recently added rows draw from on each page", () => {
    expect(text(describeRow(row("recently_added"), home))).toBe(
      "Newest movies and episodes from all libraries",
    );
    expect(text(describeRow(row("recently_added", { filter_library_ids: [1, 2] }), home))).toBe(
      "Newest additions from 2 libraries",
    );
    expect(text(describeRow(row("recently_added"), library))).toBe(
      "Newest additions to this library",
    );
    expect(text(describeRow(row("recently_added", { media_scope: "movie" }), library))).toBe(
      "Newest movies in this library",
    );
  });

  it("names the collection a collection row shows, in bold, with its kind", () => {
    const context = {
      ...home,
      collection: (id: string) =>
        id === "c1" ? { title: "Studio Ghibli", kind: "Manual" } : id === "gone" ? null : undefined,
    };
    expect(describeRow(row("collection", { library_collection_id: "c1" }), context)).toEqual([
      "The ",
      { strong: "Studio Ghibli" },
      " collection (Manual)",
    ]);
    // Not known yet (the list is still loading): no claim either way.
    expect(text(describeRow(row("collection", { library_collection_id: "c2" }), context))).toBe(
      "A collection",
    );
    expect(text(describeRow(row("collection", { library_collection_id: "gone" }), home))).toBe(
      "A collection",
    );
  });

  it("says when a collection row's collection is gone (S11)", () => {
    const context = { ...home, collection: () => null };
    expect(describeRow(row("collection", { library_collection_id: "gone" }), context)).toEqual([
      { warning: "Collection no longer available" },
    ]);
  });

  it("counts the rules of a rule row and names what it matches", () => {
    expect(
      text(
        describeRow(
          row("custom_filter", {
            media_scope: "movie",
            groups: [
              { match: "all", rules: [{}, {}] },
              { match: "all", rules: [{}] },
            ],
          }),
          home,
        ),
      ),
    ).toBe("Movies matching 3 rules");
    expect(
      text(describeRow(row("custom_filter", { groups: [{ match: "all", rules: [{}] }] }), home)),
    ).toBe("Titles matching 1 rule");
    expect(text(describeRow(row("custom_filter", { media_scope: "series" }), home))).toBe(
      "All shows",
    );
    expect(text(describeRow(row("custom_filter", { media_scope: "video" }), home))).toBe(
      "All movies and shows",
    );
  });

  it("says how a rule row with no rules is ordered, so a top-rated row is not just all movies", () => {
    const sorted = (field: string, order = "desc", media_scope = "movie") =>
      text(describeRow(row("custom_filter", { media_scope, sort: { field, order } }), library));
    expect(sorted("rating_imdb")).toBe("All movies, highest rated first");
    expect(sorted("release_date", "desc", "episode")).toBe("All episodes, newest released first");
    expect(sorted("title", "asc")).toBe("All movies, A to Z");
    expect(sorted("plays")).toBe("All movies, most played first");
    expect(sorted("added_at")).toBe("All movies");
    expect(sorted("runtime")).toBe("Movies in a chosen order");
  });

  it("never shows a raw type key for an unknown kind", () => {
    expect(text(describeRow(row("some_future_type"), home))).toBe("Row");
  });
});

describe("row copy", () => {
  it("counts titles", () => {
    expect(titleCount(20)).toBe("20 titles");
    expect(titleCount(1)).toBe("1 title");
  });

  it("labels the switch by its effect on each surface", () => {
    const shown = row("recently_added");
    expect(rowSwitchLabel("admin", { ...shown, title: "New" }, "Home")).toBe(
      "New is on for everyone",
    );
    expect(rowSwitchLabel("admin", { ...shown, title: "New", shown: false }, "Home")).toBe(
      "New is off for everyone",
    );
    expect(rowSwitchLabel("profile", { ...shown, title: "New" }, "Home")).toBe(
      "Show New on my Home",
    );
    expect(rowSwitchLabel("profile", { ...shown, title: "New" }, "Movies")).toBe(
      "Show New on my Movies page",
    );
  });

  it("says who stops seeing a collapsed row", () => {
    expect(collapsedRowText("admin", "Home")).toBe(" is off. Nobody sees this row.");
    expect(collapsedRowText("profile", "Home")).toBe(" is hidden on your Home.");
    expect(collapsedRowText("profile", "Movies")).toBe(" is hidden on your Movies page.");
  });
});

describe("collection kinds", () => {
  it("names each stored collection type the way the picker filters them", () => {
    expect(collectionKind("manual")).toBe("Manual");
    expect(collectionKind("smart")).toBe("Smart");
    for (const type of ["mdblist", "tmdb", "trakt"] as const)
      expect(collectionKind(type)).toBe("Synced list");
    expect(collectionKind(undefined)).toBeUndefined();
  });
});

describe("rule row order", () => {
  it("names how a rule row is ordered for the More options summary", () => {
    const summary = (field: string, order: string) => ruleSortSummary({ sort: { field, order } });
    expect(summary("rating_imdb", "desc")).toBe("Highest rated first");
    expect(summary("added_at", "desc")).toBe("Newest added first");
    expect(summary("title", "asc")).toBe("A to Z");
    expect(summary("runtime", "asc")).toBe("Custom order");
    expect(ruleSortSummary({ sort: "added_at", order: "desc" })).toBe("Newest added first");
  });
});

describe("rating-led row text", () => {
  // The server states each rating-led preset's rule in description_short (the
  // Add row preview shows it). The row list and the picker repeat that rule,
  // so a changed rating floor or vote minimum must change all three.
  const kinds = [
    "critically_acclaimed",
    "hidden_gems",
    "forgotten_favorites",
    "short_watches",
    "genre_roulette",
  ];
  for (const { def, preset } of everyPreset().filter(({ def }) => kinds.includes(def.type))) {
    it(`${def.type}/${preset.key} matches the server's description`, () => {
      const rule = preset.description_short.replace(/\.$/, "");
      const admin = { pageKind: "home" as const, surface: "admin" as const };
      expect(text(describeRow(row(def.type, preset.default_params), admin))).toBe(rule);
      expect(rowKindSentence(def.type).startsWith(rule)).toBe(true);
    });
  }
});
