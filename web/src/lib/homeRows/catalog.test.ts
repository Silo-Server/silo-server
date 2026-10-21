import { describe, expect, it } from "vitest";
import {
  pickerGroups,
  retiredKindLabel,
  ROW_GROUP_LABELS,
  rowKindGroup,
  rowKindLabel,
} from "./catalog";
import { recipeCatalogFixture } from "./recipeCatalogFixture.test-support";

describe("row kind catalog", () => {
  it("gives each stored row type a plain name and never a raw type key", () => {
    expect(rowKindLabel("next_up")).toBe("Next up");
    expect(rowKindLabel("custom_filter")).toBe("Titles matching rules");
    expect(rowKindLabel("award_winners")).toBe("Award winners (no longer offered)");
    expect(rowKindLabel("some_future_type")).toBe("Row");
  });

  it("files each row type under a named picker group", () => {
    expect(rowKindGroup("continue_watching")).toBe("keep");
    expect(rowKindGroup("trending_discover")).toBe("popular");
    expect(rowKindGroup("some_future_type")).toBe("collections");
    expect(ROW_GROUP_LABELS[rowKindGroup("hidden_gems")]).toBe("Moods & themes");
  });
});

describe("picker groups", () => {
  const counts = () =>
    Object.fromEntries(
      pickerGroups(recipeCatalogFixture).map((group) => [group.label, group.cards.length]),
    );

  it("offers the approved groups and card counts on every surface", () => {
    expect(counts()).toEqual({
      "Keep watching": 4,
      "What's new": 4,
      Popular: 4,
      "Picked for you": 4,
      "Moods & themes": 11,
      "Collections & rules": 2,
    });
  });

  it("never offers Editor's Picks, genre, award or Next up rows for new rows", () => {
    const withRetired = {
      categories: {
        ...recipeCatalogFixture.categories,
        discovery: [
          ...(recipeCatalogFixture.categories.discovery ?? []),
          {
            type: "next_up",
            category: "library_staples" as const,
            presets: [
              {
                key: "next_up_default",
                display_name: "On Deck",
                icon: "",
                description_short: "",
                default_params: {},
              },
            ],
            avoid_duplicates: false,
            supports_rotation: false,
            admin_only: false,
          },
          {
            type: "award_winners",
            category: "discovery" as const,
            presets: [
              {
                key: "aw_oscar",
                display_name: "Oscar Winners",
                icon: "",
                description_short: "",
                default_params: { award_type: "oscar" },
              },
            ],
            avoid_duplicates: true,
            supports_rotation: false,
            admin_only: false,
          },
        ],
      },
    };
    const types = pickerGroups(withRetired).flatMap((group) =>
      group.cards.map((card) => card.type),
    );
    expect(types).not.toContain("admin_curated_list");
    expect(types).not.toContain("genre");
    expect(types).not.toContain("award_winners");
    expect(types).not.toContain("next_up");
  });

  it("orders cards as the design lists them", () => {
    const popular = pickerGroups(recipeCatalogFixture).find((group) => group.group === "popular")!;
    expect(popular.cards.map((card) => card.label)).toEqual([
      "Trending on this server",
      "Most watched",
      "Trending worldwide",
      "What others just watched",
    ]);
  });
});

describe("retired kinds", () => {
  it("names award rows by their award", () => {
    expect(retiredKindLabel("award_winners", { award_type: "oscar" })).toBe(
      "Oscar Winners (no longer offered)",
    );
    expect(retiredKindLabel("award_winners", {})).toBe("Award winners (no longer offered)");
  });
});
