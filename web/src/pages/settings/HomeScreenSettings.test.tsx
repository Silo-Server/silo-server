import { describe, expect, it } from "vitest";

import type { SettingsSectionEntry } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";

import {
  applySectionDeletion,
  buildSectionOverrides,
  canMutateSectionSettings,
  createOverrideIdSource,
  hydrateRemovedSystemSections,
  sectionSaveErrorMessage,
} from "@/lib/homeRows/profileOverrides";
import { buildProfileGallerySection } from "@/lib/homeRows/payloads";

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

  it("keeps saved override IDs on admin sections and gives the others new ones", () => {
    const overrides = buildSectionOverrides(
      [
        makeSection({ id: "admin-1", featured: true }),
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

    expect(overrides[0]).toMatchObject({ section_id: "admin-1", featured: true });
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
});

describe("HomeScreenSettings save refusals", () => {
  it("explains a permission denial with the server's stated cause", () => {
    const refused = new V2ProblemError("replaceProfileSectionOverrides", {
      type: "https://siloserver.org/docs/api/v2/problems/permission_denied",
      title: "Permission denied",
      status: 403,
      detail: "Only an admin can add an Editor's picks row.",
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
      "Could not save your rows: Only an admin can add an Editor's picks row.",
    );
    expect(sectionSaveErrorMessage(demo)).toBe(
      "Could not save your rows: This action is not available in demo mode.",
    );
    expect(sectionSaveErrorMessage(invalid)).toBe("Could not save your rows");
    expect(sectionSaveErrorMessage(new Error("network"))).toBe("Could not save your rows");
  });
});

// Characterization for the Home rows redesign: the exact override set each
// page edit saves today, built the way the page's handlers build it.
describe("override set saved by each Home Screen edit", () => {
  const page = [
    makeSection({ id: "admin-a", title: "Recently Added", position: 0 }),
    makeSection({
      id: "admin-b",
      section_type: "continue_watching",
      title: "Continue Watching",
      position: 1,
      config: { continue_type: "watching" },
    }),
    makeSection({
      id: "own-c",
      section_type: "collection",
      title: "Mine",
      is_custom: true,
      customized: true,
      position: 2,
      config: { user_collection_id: "user-1" },
    }),
  ];
  const ids = {
    savedOverrides: [{ id: "saved-a", section_id: "admin-a", position: 0 }],
    newId: (sectionId: string) => `new-${sectionId}`,
  };
  const adminA = {
    section_id: "admin-a",
    id: "saved-a",
    hidden: false,
    title: "Recently Added",
    featured: false,
    item_limit: 20,
    section_type: undefined,
    config: { media_scope: "movie" },
  };
  const adminB = {
    section_id: "admin-b",
    id: "new-admin-b",
    hidden: false,
    title: "Continue Watching",
    featured: false,
    item_limit: 20,
    section_type: undefined,
    config: { continue_type: "watching" },
  };
  const ownC = {
    section_id: undefined,
    id: "own-c",
    hidden: false,
    title: "Mine",
    featured: false,
    item_limit: 20,
    section_type: "collection",
    config: { user_collection_id: "user-1" },
  };

  it("hides a row", () => {
    const next = page.map((s) => (s.id === "admin-b" ? { ...s, hidden: true } : s));
    expect(buildSectionOverrides(next, [], { ...ids, changedSectionId: "admin-b" })).toEqual([
      { ...adminA, position: 0 },
      { ...adminB, position: 1, hidden: true },
      { ...ownC, position: 2 },
    ]);
  });

  it("renames a row", () => {
    const next = page.map((s) => (s.id === "admin-a" ? { ...s, title: "New Movies" } : s));
    expect(buildSectionOverrides(next, [], { ...ids, changedSectionId: "admin-a" })).toEqual([
      { ...adminA, position: 0, title: "New Movies" },
      { ...adminB, position: 1 },
      { ...ownC, position: 2 },
    ]);
  });

  it("moves a row", () => {
    const next = [page[2]!, page[0]!, page[1]!];
    expect(buildSectionOverrides(next, [], { ...ids, changedSectionId: "own-c" })).toEqual([
      { ...ownC, position: 0 },
      { ...adminA, position: 1 },
      { ...adminB, position: 2 },
    ]);
  });

  it("adds a row from the gallery at the end", () => {
    const added = buildProfileGallerySection(
      {
        section_type: "trending_on_server",
        title: "Trending This Week",
        item_limit: 20,
        featured: false,
        enabled: true,
        config: { window: "7d" },
      },
      page.length,
    );
    expect(buildSectionOverrides([...page, added], [], ids)).toEqual([
      { ...adminA, position: 0 },
      { ...adminB, position: 1 },
      { ...ownC, position: 2 },
      {
        section_id: undefined,
        id: added.id,
        position: 3,
        hidden: false,
        title: "Trending This Week",
        featured: false,
        item_limit: 20,
        section_type: "trending_on_server",
        config: { window: "7d" },
      },
    ]);
  });

  it("removes a server row", () => {
    const next = applySectionDeletion(page, [], "admin-a");
    expect(buildSectionOverrides(next.sections, next.removedSystemSections, ids)).toEqual([
      { ...adminB, position: 0 },
      { ...ownC, position: 1 },
      { section_id: "admin-a", id: "saved-a", removed: true },
    ]);
  });

  it("deletes the profile's own row", () => {
    const next = applySectionDeletion(page, [], "own-c");
    expect(buildSectionOverrides(next.sections, next.removedSystemSections, ids)).toEqual([
      { ...adminA, position: 0 },
      { ...adminB, position: 1 },
    ]);
  });
});
