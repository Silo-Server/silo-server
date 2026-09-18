import { describe, expect, it } from "vitest";
import { recipeLabel } from "./EditableSectionRows";
import type { RecipeCatalogResponse } from "@/lib/recipes";

// Mirrors the trending_discover presets registered in
// internal/sections/recipes/trending_discover.go, which differ only by their
// default_params. It is the shape that exposed the bug: every variant rendered
// as the first preset's name.
const catalog = {
  categories: {
    social: [
      {
        type: "trending_discover",
        category: "social",
        avoid_duplicates: false,
        supports_rotation: false,
        admin_only: false,
        presets: [
          {
            key: "tdisc_tmdb_day",
            display_name: "TMDB Trending Today",
            icon: "🔥",
            description_short: "",
            default_params: { source: "tmdb", window: "day" },
          },
          {
            key: "tdisc_tmdb_week",
            display_name: "TMDB Trending This Week",
            icon: "🔥",
            description_short: "",
            default_params: { source: "tmdb", window: "week" },
          },
          {
            key: "tdisc_trakt",
            display_name: "Trakt Trending",
            icon: "📈",
            description_short: "",
            default_params: { source: "trakt", window: "week" },
          },
        ],
      },
    ],
  },
} as unknown as RecipeCatalogResponse;

describe("recipeLabel", () => {
  it("labels the weekly TMDB preset from its saved config", () => {
    expect(recipeLabel(catalog, "trending_discover", { source: "tmdb", window: "week" })).toBe(
      "TMDB Trending This Week",
    );
  });

  it("labels the daily TMDB preset from its saved config", () => {
    expect(recipeLabel(catalog, "trending_discover", { source: "tmdb", window: "day" })).toBe(
      "TMDB Trending Today",
    );
  });

  // Same source-agnostic window as tdisc_tmdb_week, so this only resolves if
  // every default_param is checked rather than just the distinguishing one.
  it("distinguishes Trakt from TMDB on the same window", () => {
    expect(recipeLabel(catalog, "trending_discover", { source: "trakt", window: "week" })).toBe(
      "Trakt Trending",
    );
  });

  it("ignores unrelated config keys the section also carries", () => {
    expect(
      recipeLabel(catalog, "trending_discover", {
        source: "trakt",
        window: "week",
        media_scope: "movie",
        item_limit: 20,
      }),
    ).toBe("Trakt Trending");
  });

  it("falls back to the first preset when no config is stored", () => {
    expect(recipeLabel(catalog, "trending_discover")).toBe("TMDB Trending Today");
  });

  it("falls back to the first preset when the config matches no preset", () => {
    expect(recipeLabel(catalog, "trending_discover", { source: "letterboxd" })).toBe(
      "TMDB Trending Today",
    );
  });

  it("falls back to the generic type label when the catalog has no such type", () => {
    expect(recipeLabel(catalog, "continue_watching", { continue_type: "listening" })).toBe(
      "Continue Watching",
    );
  });

  // trending_discover has no SECTION_TYPES entry, so sectionTypeLabel returns
  // the raw type string rather than a prettified one.
  it("falls back to the raw type without a catalog", () => {
    expect(recipeLabel(undefined, "trending_discover", { source: "tmdb", window: "week" })).toBe(
      "trending_discover",
    );
  });
});
