import { describe, expect, it } from "vitest";
import {
  ACCESS_GROUP_ACTION_CATEGORIES,
  ACCESS_GROUP_ACTIONS,
  ACCESS_GROUP_PRESETS,
  accessGroupSlugFromName,
  buildAccessGroupRuleDrafts,
  ruleResourceTypeForAction,
} from "./adminAccessGroupsModel";

describe("admin access groups model", () => {
  it("generates stable slugs from names", () => {
    expect(accessGroupSlugFromName("Movie Curators!")).toBe("movie_curators");
    expect(accessGroupSlugFromName("  4K Streaming  ")).toBe("group_4k_streaming");
  });

  it("maps media actions to media item rules", () => {
    expect(ruleResourceTypeForAction("metadata.curate")).toBe("media_item");
    expect(ruleResourceTypeForAction("markers.edit")).toBe("media_item");
    expect(ruleResourceTypeForAction("playback.play")).toBe("media_item");
    expect(ruleResourceTypeForAction("personal_lists.manage")).toBe("media_item");
  });

  it("builds allow rules with shared conditions", () => {
    const rules = buildAccessGroupRuleDrafts(["metadata.curate", "downloads.direct"], {
      library_ids: [1, 2],
      media_types: ["movie"],
    });

    expect(rules).toEqual([
      {
        action: "metadata.curate",
        resource_type: "media_item",
        resource_id: "*",
        effect: "allow",
        priority: 50,
        name: "Metadata Curation",
        description: "",
        conditions: { library_ids: [1, 2], media_types: ["movie"] },
      },
      {
        action: "downloads.direct",
        resource_type: "media_item",
        resource_id: "*",
        effect: "allow",
        priority: 50,
        name: "Direct Downloads",
        description: "",
        conditions: { library_ids: [1, 2], media_types: ["movie"] },
      },
    ]);
  });

  it("defines helper descriptions for every access group option", () => {
    for (const category of ACCESS_GROUP_ACTION_CATEGORIES) {
      expect(category.description.trim().length, category.label).toBeGreaterThan(24);
    }
    for (const action of ACCESS_GROUP_ACTIONS) {
      expect(action.description.trim().length, action.action).toBeGreaterThan(24);
    }
    for (const preset of ACCESS_GROUP_PRESETS) {
      expect(preset.description.trim().length, preset.id).toBeGreaterThan(24);
    }

    expect(ACCESS_GROUP_ACTIONS.find((item) => item.action === "playback.play")?.description).toBe(
      "Allows reading ebooks, listening to audiobooks, and playing video.",
    );
    expect(
      ACCESS_GROUP_ACTIONS.find((item) => item.action === "security.manage")?.description,
    ).toBe("Allows changing access groups, permissions, and security-sensitive settings.");
  });
});
