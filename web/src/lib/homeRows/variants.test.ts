import { describe, expect, it } from "vitest";
import { everyRecipe } from "./recipeCatalogFixture.test-support";
import {
  applyVariant,
  kindLocked,
  showsLabel,
  VARIANT_FAMILIES,
  variantLocked,
  variantOf,
} from "./variants";

describe("variant families", () => {
  it("covers every catalog kind that offers more than one preset", () => {
    const multi = everyRecipe()
      .filter((def) => def.presets.length > 1)
      .map((def) => def.type);
    expect(multi.sort()).toEqual(Object.keys(VARIANT_FAMILIES).sort());
  });

  it("names exactly the catalog's presets and their discriminator values", () => {
    for (const def of everyRecipe()) {
      const family = VARIANT_FAMILIES[def.type];
      if (!family) continue;
      expect(family.options.map((option) => option.presetKey)).toEqual(
        def.presets.map((preset) => preset.key),
      );
      for (const option of family.options) {
        const preset = def.presets.find((entry) => entry.key === option.presetKey)!;
        for (const [key, value] of Object.entries(option.values)) {
          expect(preset.default_params[key], `${option.presetKey}.${key}`).toEqual(value);
        }
      }
    }
  });

  it("infers each preset from its own params and turns any preset into any other exactly", () => {
    for (const def of everyRecipe()) {
      if (!VARIANT_FAMILIES[def.type]) continue;
      for (const from of def.presets) {
        expect(variantOf(def.type, from.default_params)).toBe(from.key);
        for (const to of def.presets) {
          expect(
            applyVariant(def.type, { ...from.default_params }, to.key),
            `${from.key} -> ${to.key}`,
          ).toEqual(to.default_params);
        }
      }
    }
  });

  it("keeps library filters and other keys when the variant changes", () => {
    expect(
      applyVariant(
        "trending_on_server",
        { window: "24h", filter_library_ids: [3], generated_source: "home" },
        "tr_30d",
      ),
    ).toEqual({ window: "30d", filter_library_ids: [3], generated_source: "home" });
    expect(
      applyVariant("mood_collection", { mood: "comfort", intensity: 2 }, "mood_date_night"),
    ).toEqual({
      mood: "date_night",
      intensity: 2,
    });
  });
});

describe("strict inference", () => {
  it("shows no variant for configs that match no preset", () => {
    expect(variantOf("editorial_spotlight", { subject_type: "era", subject: "1990s" })).toBeNull();
    expect(variantOf("trending_on_server", { window: "90d" })).toBeNull();
    expect(variantOf("trending_on_server", {})).toBeNull();
    expect(variantOf("format_showcase", { format: "4k", sort: "title" })).toBeNull();
    expect(variantOf("continue_watching", { continue_type: "reading" })).toBeNull();
    expect(variantOf("seasonal_themed", { theme: "christmas" })).toBeNull();
    expect(variantOf("recently_added", {})).toBeNull();
  });

  it("reads legacy single-theme seasonal rows", () => {
    expect(variantOf("seasonal_themed", { theme: "family_movie_night" })).toBe(
      "se_family_movie_night",
    );
    expect(variantOf("seasonal_themed", { enabled_themes: ["christmas"] })).toBe("se_auto");
  });

  it("ignores a mood row's intensity and a 4K row's library filter", () => {
    expect(variantOf("mood_collection", { mood: "tearjerker", intensity: 3 })).toBe(
      "mood_tearjerker",
    );
    expect(variantOf("format_showcase", { format: "hdr", filter_library_ids: [1] })).toBe("fs_hdr");
  });
});

describe("spotlight rotation", () => {
  it("keeps a monthly rotation when a director spotlight becomes an actor spotlight", () => {
    expect(
      applyVariant(
        "editorial_spotlight",
        { subject_type: "director", auto_rotate: true, rotation_cadence: "monthly", library_id: 4 },
        "es_actor",
      ),
    ).toEqual({
      subject_type: "actor",
      auto_rotate: true,
      rotation_cadence: "monthly",
      library_id: 4,
    });
  });

  it("drops the pinned decade when an era spotlight becomes a person spotlight", () => {
    expect(
      applyVariant("editorial_spotlight", { subject_type: "era", subject: "1990s" }, "es_studio"),
    ).toEqual({ subject_type: "studio", auto_rotate: true, rotation_cadence: "weekly" });
  });

  // The server needs a subject unless the row rotates, and a pinned person or
  // studio is dropped on a switch, so the row takes the preset's rotation.
  it("rotates a pinned director spotlight that becomes an actor spotlight", () => {
    for (const pinned of [{ auto_rotate: false }, {}]) {
      expect(
        applyVariant(
          "editorial_spotlight",
          { subject_type: "director", subject: "Akira Kurosawa", library_id: 4, ...pinned },
          "es_actor",
        ),
      ).toEqual({
        subject_type: "actor",
        auto_rotate: true,
        rotation_cadence: "weekly",
        library_id: 4,
      });
    }
  });

  it("pins The 80s and drops rotation", () => {
    expect(
      applyVariant(
        "editorial_spotlight",
        { subject_type: "director", auto_rotate: true, rotation_cadence: "monthly", library_id: 4 },
        "es_era_80s",
      ),
    ).toEqual({ subject_type: "era", subject: "1980s", library_id: 4 });
  });
});

describe("seasonal themes", () => {
  it("replaces a legacy single theme only when the user picks a variant", () => {
    expect(
      applyVariant(
        "seasonal_themed",
        { theme: "christmas", theme_titles: { x: "y" } },
        "se_family_movie_night",
      ),
    ).toEqual({ enabled_themes: ["family_movie_night"], theme_titles: { x: "y" } });
  });
});

describe("Shows line", () => {
  it("names a legacy single-theme seasonal row's holiday", () => {
    expect(showsLabel("seasonal_themed", { theme: "christmas" })).toBe(
      "Seasonal picks (Christmas only)",
    );
    expect(showsLabel("seasonal_themed", { theme: "unknown_day" })).toBe("Seasonal picks");
  });

  it("locks the kind only for legacy Trakt rows; Continue Reading can still change kind", () => {
    const reading = { continue_type: "reading" };
    expect(variantLocked("continue_watching", reading)).toBe(true);
    expect(kindLocked(reading)).toBe(false);
    expect(kindLocked({ source: "trakt", window: "week" })).toBe(true);
  });
});
