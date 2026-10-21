import { describe, expect, it } from "vitest";
import { describeRow } from "./describe";
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
  };
}

const home = { pageKind: "home" as const };

function text(parts: ReturnType<typeof describeRow>): string {
  return parts
    .map((part) =>
      typeof part === "string" ? part : "strong" in part ? part.strong : part.warning,
    )
    .join("");
}

describe("describeRow", () => {
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

  it("never shows a raw type key for an unknown kind", () => {
    expect(text(describeRow(row("some_future_type"), home))).toBe("Row");
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
