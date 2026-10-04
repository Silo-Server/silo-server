// Characterization tests for the row labels the section lists show (#1503).
// The redesign's names must keep these, or change them with a stated reason.
import { describe, expect, it } from "vitest";

import { recipeLabel } from "@/components/sections/EditableSectionRows";

import { everyPreset, recipeCatalogFixture } from "./recipeCatalogFixture.test-support";

describe("recipe labels", () => {
  it.each(everyPreset().map(({ def, preset }) => [def.type, preset.key, preset] as const))(
    "names a %s row saved from %s by its preset",
    (type, _key, preset) => {
      expect(recipeLabel(recipeCatalogFixture, type, preset.default_params)).toBe(
        preset.display_name,
      );
    },
  );

  it("keeps a matching preset's name when the row has extra keys", () => {
    expect(
      recipeLabel(recipeCatalogFixture, "mood_collection", { mood: "comfort", intensity: "high" }),
    ).toBe("Comfort Rewatches");
    expect(
      recipeLabel(recipeCatalogFixture, "recently_added", {
        filter_library_id: 2,
        generated_source: "home_library_recent",
      }),
    ).toBe("Recently Added");
  });

  // Configs no preset matches fall back to the type's first preset. Some of
  // these names are wrong for the row; the redesign's strict variants fix
  // that, and these record today's names until then.
  it.each([
    ["trending_on_server", { window: "90d" }, "Trending Now (24h)"],
    ["editorial_spotlight", { subject_type: "era", subject: "1990s" }, "Director Spotlight"],
    [
      "editorial_spotlight",
      { subject_type: "director", auto_rotate: true, rotation_cadence: "monthly" },
      "Director Spotlight",
    ],
    [
      "editorial_spotlight",
      { subject_type: "actor", auto_rotate: true, rotation_cadence: "monthly" },
      "Director Spotlight",
    ],
    ["seasonal_themed", { theme: "family_movie_night" }, "Seasonal Picks"],
    ["seasonal_themed", { theme: "christmas" }, "Seasonal Picks"],
    ["continue_watching", { continue_type: "reading" }, "Continue Watching"],
    ["trending_discover", { source: "tmdb", window: "month" }, "TMDB Trending Today"],
    ["trending_discover", { source: "trakt", window: "week" }, "TMDB Trending Today"],
  ] as const)("labels a drifted %s row %j as %s", (type, config, label) => {
    expect(recipeLabel(recipeCatalogFixture, type, config)).toBe(label);
  });

  it("names types the gallery doesn't list from the static list, else by the raw type", () => {
    expect(recipeLabel(recipeCatalogFixture, "genre", {})).toBe("Genre");
    expect(recipeLabel(recipeCatalogFixture, "award_winners", { award_type: "oscar" })).toBe(
      "award_winners",
    );
    expect(recipeLabel(recipeCatalogFixture, "no_such_type", {})).toBe("no_such_type");
  });

  it("names rows from the static list before the catalog loads", () => {
    expect(recipeLabel(undefined, "recently_released", {})).toBe("Recently Released");
    expect(recipeLabel(undefined, "trending_discover", { window: "week" })).toBe(
      "trending_discover",
    );
  });
});
