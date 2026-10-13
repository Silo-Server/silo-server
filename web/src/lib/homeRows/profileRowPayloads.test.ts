import { describe, expect, it } from "vitest";
import type { SettingsSectionEntry } from "@/api/types";
import { buildProfileRowCreate, buildProfileRowUpdate } from "./payloads";
import type { RowDraft } from "./rowDraft";

function draft(overrides: Partial<RowDraft> = {}): RowDraft {
  return {
    sectionType: "trending_on_server",
    title: "Trending This Week",
    titleFollowsVariant: true,
    config: { window: "7d" },
    itemLimit: 20,
    hero: false,
    ...overrides,
  };
}

function entry(overrides: Partial<SettingsSectionEntry> = {}): SettingsSectionEntry {
  return {
    id: "row-1",
    section_type: "recently_added",
    title: "Recently Added",
    default_title: "Recently Added",
    featured: false,
    item_limit: 20,
    hidden: false,
    is_custom: false,
    customized: false,
    position: 2,
    config: { filter_library_ids: [3], generated_library_id: 9 },
    ...overrides,
  };
}

describe("buildProfileRowCreate", () => {
  it("creates a profile-owned row at the requested position", () => {
    const created = buildProfileRowCreate(draft(), "Trending This Week", 5);
    expect(created).toEqual({
      id: expect.any(String),
      section_type: "trending_on_server",
      title: "Trending This Week",
      item_limit: 20,
      featured: false,
      config: { window: "7d" },
      is_custom: true,
      customized: true,
      hidden: false,
      position: 5,
    });
  });
});

describe("buildProfileRowUpdate", () => {
  it("keeps the stored config byte for byte when only the name changes", () => {
    const section = entry();
    const updated = buildProfileRowUpdate(
      section,
      draft({ sectionType: "recently_added", config: { ...section.config }, title: "Mine" }),
      "Mine",
    );
    expect(updated.config).toBe(section.config);
    expect(updated).toMatchObject({
      id: "row-1",
      section_type: "recently_added",
      title: "Mine",
      default_title: "Recently Added",
      is_custom: false,
      hidden: false,
      position: 2,
    });
  });

  it("keeps a personal collection row's id key on a rename", () => {
    const section = entry({
      section_type: "collection",
      is_custom: true,
      config: { user_collection_id: "mine-1", generated_source: "x" },
    });
    const updated = buildProfileRowUpdate(
      section,
      draft({ sectionType: "collection", config: { ...section.config }, title: "Renamed" }),
      "Renamed",
    );
    expect(updated.config).toEqual({ user_collection_id: "mine-1", generated_source: "x" });
  });

  it("saves a changed variant, hero and size, and keeps the row hidden if it was", () => {
    const section = entry({
      section_type: "trending_on_server",
      title: "Trending",
      hidden: true,
      config: { window: "7d" },
    });
    const updated = buildProfileRowUpdate(
      section,
      draft({ config: { window: "24h" }, hero: true, itemLimit: 30 }),
      "Trending Today",
    );
    expect(updated).toMatchObject({
      config: { window: "24h" },
      featured: true,
      item_limit: 30,
      hidden: true,
      title: "Trending Today",
    });
  });

  it("changes what the profile's own row shows", () => {
    const section = entry({ section_type: "hidden_gems", is_custom: true, config: {} });
    const updated = buildProfileRowUpdate(
      section,
      draft({ sectionType: "random", config: {} }),
      "Surprise me",
    );
    expect(updated).toMatchObject({ section_type: "random", config: {}, is_custom: true });
  });
});
