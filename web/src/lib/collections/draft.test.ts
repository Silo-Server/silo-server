import { describe, expect, it } from "vitest";

import { changedFields, clearDefaultSort, mergeDraft, takeFields } from "./draft";
import type { CollectionDraft } from "./scope";

const base: CollectionDraft = {
  kind: "manual",
  name: "Studio Ghibli",
  description: "Every film",
  libraryIds: [1, 2],
  artwork: {},
  server: { visibility: "visible" },
};

describe("changedFields", () => {
  it("names each field that differs, and ignores library order", () => {
    expect(changedFields(base, { ...base, libraryIds: [2, 1] })).toEqual([]);
    expect(
      changedFields(base, {
        ...base,
        name: "Ghibli",
        description: "",
        server: { visibility: "hidden" },
      }),
    ).toEqual(["name", "description", "visibility"]);
  });

  it("ignores key order inside rules and staged artwork", () => {
    const smart: CollectionDraft = {
      ...base,
      kind: "smart",
      rawSortConfig: { field: "title", order: "asc" },
    };
    expect(
      changedFields(smart, {
        ...smart,
        rawSortConfig: { order: "asc", field: "title" },
        artwork: { poster: { sourceUrl: "https://images.example/p.png" } },
      }),
    ).toEqual([]);
  });
});

describe("mergeDraft", () => {
  it.each([
    [
      "only theirs changed: the draft takes the server's value",
      { name: "Studio Ghibli" },
      { name: "Ghibli (server)" },
      { name: "Ghibli (server)" },
      [],
    ],
    [
      "only mine changed: the draft keeps mine",
      { name: "Ghibli (mine)" },
      { name: "Studio Ghibli" },
      { name: "Ghibli (mine)" },
      [],
    ],
    [
      "both changed to the same value: no conflict",
      { name: "Ghibli" },
      { name: "Ghibli" },
      { name: "Ghibli" },
      [],
    ],
    [
      "both changed differently: keeps mine and reports the field",
      { name: "Ghibli (mine)" },
      { name: "Ghibli (server)" },
      { name: "Ghibli (mine)" },
      ["name"],
    ],
    [
      "an untouched field takes the server value even when another conflicts",
      { name: "Ghibli (mine)" },
      { name: "Ghibli (server)", description: "Changed elsewhere" },
      { name: "Ghibli (mine)", description: "Changed elsewhere" },
      ["name"],
    ],
  ] as const)("%s", (_label, mine, theirs, expected, conflicts) => {
    const result = mergeDraft(base, { ...base, ...mine }, { ...base, ...theirs });
    expect(result.draft).toMatchObject(expected);
    expect(result.conflicts).toEqual(conflicts);
    expect(result.base).toEqual({ ...base, ...theirs });
  });

  it("merges switches inside the scope groups and keeps staged artwork", () => {
    const personal: CollectionDraft = {
      kind: "manual",
      name: "Comfort",
      description: "",
      libraryIds: [],
      artwork: {},
      personal: { shared: true, inLibraryTabs: false },
    };
    const poster = { sourceUrl: "https://images.example/p.png" };
    const result = mergeDraft(
      personal,
      { ...personal, personal: { shared: false, inLibraryTabs: false }, artwork: { poster } },
      { ...personal, personal: { shared: true, inLibraryTabs: true } },
    );
    expect(result.draft.personal).toEqual({ shared: false, inLibraryTabs: true });
    expect(result.draft.artwork).toEqual({ poster });
    expect(result.conflicts).toEqual([]);
  });
});

it("takeFields copies only the named fields", () => {
  const theirs = { ...base, name: "Theirs", description: "Theirs too" };
  expect(takeFields({ ...base, name: "Mine", description: "Mine too" }, theirs, ["name"])).toEqual({
    ...base,
    name: "Theirs",
    description: "Mine too",
  });
});

describe("smart rules and their libraries", () => {
  const smart: CollectionDraft = {
    ...base,
    kind: "smart",
    rules: {
      library_ids: [1, 2],
      match: "all",
      groups: [],
      sort: { field: "added_at", order: "desc" },
    },
  };

  it("counts a library change once, as Libraries, not as Rules too", () => {
    expect(
      changedFields(smart, {
        ...smart,
        libraryIds: [1],
        rules: { ...smart.rules!, library_ids: [1] },
      }),
    ).toEqual(["libraryIds"]);
  });

  it("still sees a rule change", () => {
    expect(changedFields(smart, { ...smart, rules: { ...smart.rules!, limit: 50 } })).toEqual([
      "rules",
    ]);
  });
});

describe("clearDefaultSort", () => {
  it("drops the stored sort and keeps every other setting", () => {
    expect(clearDefaultSort({ field: "title", order: "asc", mode: "manual_pins" })).toEqual({
      mode: "manual_pins",
    });
    expect(clearDefaultSort(undefined)).toEqual({});
  });
});

describe("a saved Synced list's fields", () => {
  const synced: CollectionDraft = {
    ...base,
    kind: "synced",
    list: {
      source: "mdblist",
      link: "https://mdblist.com/lists/u/top",
      franchiseId: "",
      limit: 50,
      schedule: "0 3 * * *",
      stored: { mode: "mdblist_json", url: "https://mdblist.com/lists/u/top", limit: 50 },
    },
  };

  it("names the list, max titles and the schedule as separate fields", () => {
    expect(
      changedFields(synced, {
        ...synced,
        list: { ...synced.list!, link: "https://mdblist.com/lists/u/new", limit: 10, schedule: "" },
      }),
    ).toEqual(["list", "limit", "schedule"]);
  });

  it("takes a new schedule from the server while keeping a typed link", () => {
    const mine = { ...synced, list: { ...synced.list!, link: "https://mdblist.com/lists/u/new" } };
    const theirs = { ...synced, list: { ...synced.list!, schedule: "0 4 * * *" } };
    const merged = mergeDraft(synced, mine, theirs);
    expect(merged.conflicts).toEqual([]);
    expect(merged.draft.list).toMatchObject({
      link: "https://mdblist.com/lists/u/new",
      schedule: "0 4 * * *",
    });
  });
});
