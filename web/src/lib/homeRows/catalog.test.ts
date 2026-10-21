import { describe, expect, it } from "vitest";
import { pickerGroups, rowKindLabel } from "./catalog";
import { recipeCatalogFixture } from "./recipeCatalogFixture.test-support";

describe("row kind catalog", () => {
  it("gives each stored row type a plain name and never a raw type key", () => {
    expect(rowKindLabel("next_up")).toBe("Next up");
    expect(rowKindLabel("custom_filter")).toBe("Titles matching rules");
    expect(rowKindLabel("award_winners")).toBe("Award winners (no longer offered)");
    expect(rowKindLabel("some_future_type")).toBe("Row");
  });
});

describe("picker groups", () => {
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
});
