import { describe, expect, it } from "vitest";

import type { SettingsSectionEntry } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";

import {
  applySectionDeletion,
  canMutateSectionSettings,
  buildSectionOverrides,
  buildProfileGallerySection,
  canAddAdminOnlyRecipes,
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

  it("explains a refused custom section instead of a generic save failure", () => {
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

    expect(sectionSaveErrorMessage(refused)).toBe(
      "This server does not allow profiles to build custom sections. Ask an admin to allow them.",
    );
    expect(sectionSaveErrorMessage(invalid)).toBe("Failed to save section changes");
    expect(sectionSaveErrorMessage(new Error("network"))).toBe("Failed to save section changes");
  });
});
