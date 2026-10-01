import { describe, expect, it } from "vitest";

import type { SettingsSectionEntry } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { canAddAdminOnlyRecipes } from "@/lib/sectionTypes";

import {
  applySectionDeletion,
  canMutateSectionSettings,
  buildSectionOverrides,
  createOverrideIdSource,
  buildProfileGallerySection,
  hydrateRemovedSystemSections,
  sectionSaveErrorMessage,
  shouldRestoreLatestSaveFailure,
  shouldRestoreSelectionState,
} from "./HomeScreenSettings";

function makeSection(overrides: Partial<SettingsSectionEntry> = {}): SettingsSectionEntry {
  return {
    id: overrides.id ?? "section-1",
    section_type: overrides.section_type ?? "recently_added",
    title: overrides.title ?? "Recently Added",
    featured: overrides.featured ?? false,
    item_limit: overrides.item_limit ?? 20,
    hidden: overrides.hidden ?? false,
    is_custom: overrides.is_custom ?? false,
    customized: overrides.customized ?? false,
    position: overrides.position ?? 0,
    config: overrides.config ?? { media_scope: "movie" },
  };
}

describe("HomeScreenSettings helpers", () => {
  it("serializes featured section overrides for persistence", () => {
    const overrides = buildSectionOverrides([
      makeSection({ id: "admin-1", featured: true }),
      makeSection({ id: "custom-1", is_custom: true, featured: true, section_type: "genre" }),
    ]);

    expect(overrides).toEqual([
      expect.objectContaining({
        section_id: "admin-1",
        featured: true,
      }),
      expect.objectContaining({
        id: "custom-1",
        section_type: "genre",
        featured: true,
      }),
    ]);
  });

  it("serializes custom gallery sections as profile-owned overrides without enabled state", () => {
    const section = buildProfileGallerySection(
      {
        section_type: "hidden_gems",
        title: "Hidden Gems",
        item_limit: 18,
        featured: true,
        enabled: false,
        config: { mood: "underrated" },
      },
      3,
    );
    const [override] = buildSectionOverrides([section]);

    expect(section).toMatchObject({
      section_type: "hidden_gems",
      title: "Hidden Gems",
      featured: true,
      is_custom: true,
      position: 3,
      config: { mood: "underrated" },
    });
    expect(override).toMatchObject({
      id: section.id,
      section_type: "hidden_gems",
      title: "Hidden Gems",
      featured: true,
      item_limit: 18,
      config: { mood: "underrated" },
    });
    expect(override).not.toHaveProperty("enabled");
  });

  it("serializes removed system sections as removed overrides", () => {
    const overrides = buildSectionOverrides(
      [
        makeSection({ id: "admin-1", title: "Recently Added" }),
        makeSection({ id: "custom-1", is_custom: true, title: "Custom Picks" }),
      ],
      [{ id: "admin-2" }],
    );

    expect(overrides).toContainEqual(
      expect.objectContaining({
        section_id: "admin-2",
        removed: true,
      }),
    );
  });

  it("keeps saved override IDs on admin sections and gives the others new ones", () => {
    const overrides = buildSectionOverrides(
      [
        makeSection({ id: "admin-1" }),
        makeSection({ id: "admin-2" }),
        makeSection({ id: "custom-1", is_custom: true }),
      ],
      [{ id: "admin-3" }, { id: "admin-4" }],
      {
        savedOverrides: [
          { id: "stale", section_id: "admin-1" },
          { id: "saved-1", section_id: "admin-1", position: 0 },
          { id: "saved-3", section_id: "admin-3", removed: true },
          { id: "custom-1", position: 1 },
        ],
        newId: (sectionId) => `new-${sectionId}`,
      },
    );

    expect(overrides.map(({ id, section_id }) => ({ id, section_id }))).toEqual([
      { id: "saved-1", section_id: "admin-1" },
      { id: "new-admin-2", section_id: "admin-2" },
      { id: "custom-1", section_id: undefined },
      { id: "saved-3", section_id: "admin-3" },
      { id: "new-admin-4", section_id: "admin-4" },
    ]);
  });

  it("leaves out a shown legacy Trakt section without a saved override unless the change is to it", () => {
    const trakt = { source: "trakt", list: "trending" };
    const sections = [
      makeSection({ id: "trakt-shown", config: trakt }),
      makeSection({ id: "trakt-hidden", config: trakt, hidden: true }),
      makeSection({ id: "trakt-saved", config: { source_provider: "trakt" } }),
      makeSection({ id: "admin-1" }),
    ];
    const ids = {
      savedOverrides: [{ id: "saved", section_id: "trakt-saved" }],
      newId: (sectionId: string) => `new-${sectionId}`,
    };
    const overrides = buildSectionOverrides(sections, [{ id: "trakt-removed" }], {
      ...ids,
      changedSectionId: "admin-1",
    });

    expect(overrides.map(({ id, section_id }) => ({ id, section_id }))).toEqual([
      { id: "new-trakt-hidden", section_id: "trakt-hidden" },
      { id: "saved", section_id: "trakt-saved" },
      { id: "new-admin-1", section_id: "admin-1" },
      { id: "new-trakt-removed", section_id: "trakt-removed" },
    ]);
    expect(overrides.find((o) => o.section_id === "admin-1")?.position).toBe(3);

    // An edit or move of the section itself is sent, so its refusal shows.
    const edited = buildSectionOverrides(sections, [], { ...ids, changedSectionId: "trakt-shown" });
    expect(edited[0]).toMatchObject({ id: "new-trakt-shown", section_id: "trakt-shown" });
  });

  it("numbers the other sections around a left-out section's admin position", () => {
    const held = makeSection({ id: "trakt", position: 3, config: { source: "trakt" } });
    const row = (id: string) => makeSection({ id });
    const positions = (sections: SettingsSectionEntry[]) =>
      buildSectionOverrides(sections, [], { newId: (id) => `new-${id}` }).map(
        ({ section_id, position }) => [section_id, position],
      );

    // A row moved below the held section follows it.
    expect(positions([row("a"), row("b"), held, row("moved"), row("c")])).toEqual([
      ["a", 0],
      ["b", 1],
      ["moved", 4],
      ["c", 5],
    ]);
    // With more rows above it than its position allows, it keeps its place and
    // the rest follow it, never sharing its position.
    expect(positions([row("moved"), row("a"), row("b"), row("c"), held, row("d")])).toEqual([
      ["moved", 0],
      ["a", 1],
      ["b", 2],
      ["c", 4],
      ["d", 5],
    ]);
  });

  it("gives each admin section one new override ID per source", () => {
    const first = createOverrideIdSource();
    const id = first("admin-1");

    expect(first("admin-1")).toBe(id);
    expect(first("admin-2")).not.toBe(id);
    expect(createOverrideIdSource()("admin-1")).not.toBe(id);
  });

  it("removes a deleted custom section from visible rows without adding a removed override", () => {
    const result = applySectionDeletion(
      [
        makeSection({ id: "admin-1", title: "Recently Added" }),
        makeSection({ id: "custom-1", is_custom: true, title: "Custom Picks" }),
        makeSection({ id: "admin-2", title: "Continue Watching" }),
      ],
      [],
      "custom-1",
    );

    expect(result.sections.map((section) => section.id)).toEqual(["admin-1", "admin-2"]);
    expect(result.removedSystemSections).toEqual([]);
  });

  it("removes a deleted system section from visible rows while retaining a removed override", () => {
    const result = applySectionDeletion(
      [
        makeSection({ id: "admin-1", title: "Recently Added" }),
        makeSection({ id: "custom-1", is_custom: true, title: "Custom Picks" }),
        makeSection({ id: "admin-2", title: "Continue Watching" }),
      ],
      [],
      "admin-2",
    );

    expect(result.sections.map((section) => section.id)).toEqual(["admin-1", "custom-1"]);
    expect(result.removedSystemSections).toEqual([{ id: "admin-2" }]);
  });

  it("hydrates removed system sections from raw overrides and reserializes them after a fresh load", () => {
    const removedSystemSections = hydrateRemovedSystemSections([
      { section_id: "admin-2", removed: true },
      { id: "custom-1", removed: true },
      { section_id: "admin-3", hidden: true },
    ]);

    expect(removedSystemSections).toEqual([{ id: "admin-2" }]);

    const overrides = buildSectionOverrides(
      [
        makeSection({ id: "admin-1", title: "Recently Added" }),
        makeSection({ id: "custom-2", is_custom: true, title: "Custom Picks" }),
      ],
      removedSystemSections,
    );

    expect(overrides).toContainEqual(
      expect.objectContaining({
        section_id: "admin-2",
        removed: true,
      }),
    );
  });

  it("disables save-producing section actions until raw overrides are ready", () => {
    expect(
      canMutateSectionSettings(
        { isSuccess: false, isError: false },
        { isSuccess: false, isError: false },
      ),
    ).toBe(false);
    expect(
      canMutateSectionSettings(
        { isSuccess: true, isError: false },
        { isSuccess: false, isError: false },
      ),
    ).toBe(false);
    expect(
      canMutateSectionSettings(
        { isSuccess: true, isError: false },
        { isSuccess: true, isError: false },
      ),
    ).toBe(true);
  });

  it("only restores rollback state when the selection identity still matches", () => {
    expect(shouldRestoreSelectionState("library:1", "library:1")).toBe(true);
    expect(shouldRestoreSelectionState("library:2", "library:1")).toBe(false);
    expect(shouldRestoreSelectionState("home", "library:1")).toBe(false);
  });

  it("only restores rollback state for the latest save attempt in the current selection", () => {
    expect(shouldRestoreLatestSaveFailure("library:1", "library:1", 3, 3)).toBe(true);
    expect(shouldRestoreLatestSaveFailure("library:1", "library:1", 4, 3)).toBe(false);
    expect(shouldRestoreLatestSaveFailure("library:2", "library:1", 3, 3)).toBe(false);
  });
});

describe("HomeScreenSettings custom section permission", () => {
  it("offers admin-only recipes to admins and to profiles the server allows", () => {
    expect(canAddAdminOnlyRecipes("admin", false)).toBe(true);
    expect(canAddAdminOnlyRecipes("admin", undefined)).toBe(true);
    expect(canAddAdminOnlyRecipes("user", true)).toBe(true);
  });

  it("withholds admin-only recipes from other profiles, including before the flag loads", () => {
    expect(canAddAdminOnlyRecipes("user", false)).toBe(false);
    expect(canAddAdminOnlyRecipes("user", undefined)).toBe(false);
    expect(canAddAdminOnlyRecipes(undefined, undefined)).toBe(false);
  });

  it("explains a permission denial with the server's stated cause", () => {
    const refused = new V2ProblemError("replaceProfileSectionOverrides", {
      type: "https://siloserver.org/docs/api/v2/problems/permission_denied",
      title: "Permission denied",
      status: 403,
      detail: "this server does not allow profiles to build custom sections",
      instance: "test",
    });
    const invalid = new V2ProblemError("replaceProfileSectionOverrides", {
      type: "https://siloserver.org/docs/api/v2/problems/validation_failed",
      title: "Invalid",
      status: 422,
      detail: "library_ids: at least one",
      instance: "test",
    });

    const demo = new V2ProblemError("replaceProfileSectionOverrides", {
      type: "https://siloserver.org/docs/api/v2/problems/permission_denied",
      title: "Permission denied",
      status: 403,
      detail: "This action is not available in demo mode.",
      instance: "test",
    });

    expect(sectionSaveErrorMessage(refused)).toBe(
      "Failed to save section changes: this server does not allow profiles to build custom sections",
    );
    expect(sectionSaveErrorMessage(demo)).toBe(
      "Failed to save section changes: This action is not available in demo mode.",
    );
    expect(sectionSaveErrorMessage(invalid)).toBe("Failed to save section changes");
    expect(sectionSaveErrorMessage(new Error("network"))).toBe("Failed to save section changes");
  });
});
