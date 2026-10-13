import { describe, expect, it } from "vitest";
import type { SettingsSectionEntry } from "@/api/types";
import { buildSectionOverrides } from "./profileOverrides";

function entry(id: string, overrides: Partial<SettingsSectionEntry> = {}): SettingsSectionEntry {
  return {
    id,
    section_type: "recently_added",
    title: id,
    featured: false,
    item_limit: 20,
    hidden: false,
    is_custom: false,
    customized: false,
    position: 0,
    config: {},
    ...overrides,
  };
}

describe("buildSectionOverrides with several changed rows", () => {
  const trakt = { source: "trakt", list: "trending" };
  const sections = [
    entry("a"),
    entry("trakt-1", { position: 1, config: trakt }),
    entry("trakt-2", { position: 2, config: trakt }),
  ];
  const newId = (id: string) => `new-${id}`;

  it("sends each shown legacy Trakt row the merged changes name, and leaves out the rest", () => {
    const overrides = buildSectionOverrides(sections, [], {
      newId,
      changedSectionIds: new Set(["a", "trakt-2"]),
    });
    expect(overrides.map((o) => [o.section_id, o.position])).toEqual([
      ["a", 0],
      ["trakt-2", 2],
    ]);
  });

  it("matches the single changed row form when the set holds one id", () => {
    expect(
      buildSectionOverrides(sections, [], { newId, changedSectionIds: new Set(["trakt-1"]) }),
    ).toEqual(buildSectionOverrides(sections, [], { newId, changedSectionId: "trakt-1" }));
  });
});

// O3: with the page as it was read, a save stores only what the profile changed,
// so later admin edits to the rest of a row still reach this profile.
describe("buildSectionOverrides against the page as it was read", () => {
  const newId = (id: string) => `new-${id}`;
  const page = [
    entry("a", { title: "Recently Added", default_title: "Recently Added" }),
    entry("b", {
      section_type: "trending_on_server",
      title: "Trending",
      default_title: "Trending",
      position: 1,
      config: { window: "7d" },
    }),
    entry("own", {
      section_type: "hidden_gems",
      title: "Gems",
      is_custom: true,
      customized: true,
      position: 2,
      config: { mood: "underrated" },
    }),
  ];
  const ownOverride = {
    section_id: undefined,
    id: "own",
    position: 2,
    hidden: false,
    title: "Gems",
    featured: false,
    item_limit: 20,
    section_type: "hidden_gems",
    config: { mood: "underrated" },
  };
  const build = (
    sections: SettingsSectionEntry[],
    options: Parameters<typeof buildSectionOverrides>[2] = {},
    removed: Array<{ id: string }> = [],
  ) => buildSectionOverrides(sections, removed, { newId, baseline: page, ...options });

  it("stores only the hide of a server row, without its title, size, hero or config", () => {
    const next = page.map((s) => (s.id === "b" ? { ...s, hidden: true } : s));
    expect(build(next, { changedSectionId: "b" })).toEqual([
      { section_id: "b", id: "new-b", hidden: true },
      ownOverride,
    ]);
  });

  it("stores a rename as the title alone", () => {
    const next = page.map((s) => (s.id === "a" ? { ...s, title: "New Movies" } : s));
    expect(build(next, { changedSectionId: "a" })).toEqual([
      { section_id: "a", id: "new-a", hidden: false, title: "New Movies" },
      ownOverride,
    ]);
  });

  it("stores each changed field and nothing the profile left alone", () => {
    const next = page.map((s) =>
      s.id === "b" ? { ...s, featured: true, item_limit: 30, config: { window: "24h" } } : s,
    );
    expect(build(next, { changedSectionId: "b" })[0]).toEqual({
      section_id: "b",
      id: "new-b",
      hidden: false,
      featured: true,
      item_limit: 30,
      config: { window: "24h" },
    });
  });

  it("follows the server's name again when the profile takes the original name back", () => {
    const renamed = page.map((s) => (s.id === "a" ? { ...s, title: "Mine" } : s));
    const saved = [{ id: "saved-a", section_id: "a", title: "Mine" }];
    const next = renamed.map((s) => (s.id === "a" ? { ...s, title: "Recently Added" } : s));
    expect(
      buildSectionOverrides(next, [], {
        newId,
        baseline: renamed,
        savedOverrides: saved,
        changedSectionId: "a",
      })[0],
    ).toEqual({ section_id: "a", id: "saved-a", hidden: false });
  });

  it("leaves fields an earlier save pinned as they are", () => {
    const saved = [
      {
        id: "saved-b",
        section_id: "b",
        position: 1,
        title: "Trending",
        featured: false,
        item_limit: 20,
        config: { window: "7d" },
      },
    ];
    const next = page.map((s) => (s.id === "a" ? { ...s, hidden: true } : s));
    const overrides = build(next, { savedOverrides: saved, changedSectionId: "a" });
    expect(overrides.find((o) => o.section_id === "b")).toEqual({
      section_id: "b",
      id: "saved-b",
      position: 1,
      hidden: false,
      title: "Trending",
      featured: false,
      item_limit: 20,
      config: { window: "7d" },
    });
  });

  it("keeps what an earlier save without an ID stored, under a new ID", () => {
    const saved = [{ id: "", section_id: "b", title: "Hot", item_limit: 30 }];
    const next = page.map((s) => (s.id === "a" ? { ...s, hidden: true } : s));
    const overrides = build(next, { savedOverrides: saved, changedSectionId: "a" });
    expect(overrides.find((o) => o.section_id === "b")).toEqual({
      section_id: "b",
      id: "new-b",
      hidden: false,
      title: "Hot",
      item_limit: 30,
    });
  });

  it("stores every row's position once the profile moves a row", () => {
    const next = [page[1]!, page[0]!, page[2]!];
    expect(build(next, { changedSectionId: "b" })).toEqual([
      { section_id: "b", id: "new-b", position: 0, hidden: false },
      { section_id: "a", id: "new-a", position: 1, hidden: false },
      ownOverride,
    ]);
  });

  it("keeps storing positions after an earlier save stored them", () => {
    const saved = [{ id: "saved-a", section_id: "a", position: 0 }];
    const next = page.map((s) => (s.id === "b" ? { ...s, hidden: true } : s));
    expect(
      build(next, { savedOverrides: saved, changedSectionId: "b" }).map((o) => [
        o.section_id ?? o.id,
        o.position,
      ]),
    ).toEqual([
      ["a", 0],
      ["b", 1],
      ["own", 2],
    ]);
  });

  it("stores a row the profile added with all of its fields", () => {
    const added = entry("added", {
      section_type: "random",
      title: "Surprise",
      is_custom: true,
      customized: true,
      position: 3,
      config: {},
    });
    expect(build([...page, added], { changedSectionId: "added" }).at(-1)).toEqual({
      section_id: undefined,
      id: "added",
      position: 3,
      hidden: false,
      title: "Surprise",
      featured: false,
      item_limit: 20,
      section_type: "random",
      config: {},
    });
  });

  describe("a row added since the page was read", () => {
    const added = (id: string, position: number) =>
      entry(id, { section_type: "random", title: id, is_custom: true, customized: true, position });

    it("keeps the admin order while the new rows stay at the bottom in order", () => {
      const overrides = build([...page, added("x", 3), added("y", 4)], {
        changedSectionIds: new Set(["x", "y"]),
      });
      expect(overrides.map((o) => [o.section_id ?? o.id, o.position])).toEqual([
        ["own", 2],
        ["x", 3],
        ["y", 4],
      ]);
    });

    it("stores every position once a new row moves above another row", () => {
      const overrides = build([page[0]!, added("x", 3), page[1]!, page[2]!], {
        changedSectionId: "x",
      });
      expect(overrides.map((o) => [o.section_id ?? o.id, o.position])).toEqual([
        ["a", 0],
        ["x", 1],
        ["b", 2],
        ["own", 3],
      ]);
    });

    it("stores every position once two new rows swap", () => {
      const overrides = build([...page, added("y", 4), added("x", 3)], {
        changedSectionIds: new Set(["x", "y"]),
      });
      expect(overrides.map((o) => [o.section_id ?? o.id, o.position])).toEqual([
        ["a", 0],
        ["b", 1],
        ["own", 2],
        ["y", 3],
        ["x", 4],
      ]);
    });
  });

  it("stores a removed server row as removed and nothing for rows left alone", () => {
    expect(build(page.slice(1), { changedSectionId: "a" }, [{ id: "a" }])).toEqual([
      ownOverride,
      { section_id: "a", id: "new-a", removed: true },
    ]);
  });
});
