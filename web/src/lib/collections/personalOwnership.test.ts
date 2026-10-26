import { describe, expect, it } from "vitest";

import { isOwnCollection, partitionPersonalCollections } from "./personalOwnership";

const profiles = [
  { id: "p-kid", name: "Kid" },
  { id: "p-me", name: "Me" },
  { id: "p-parent", name: "Parent" },
];

function collection(id: string, creator: string) {
  return { id, creator_profile_id: creator };
}

describe("personal collection ownership", () => {
  it("treats only the creator's collections as its own", () => {
    expect(isOwnCollection(collection("a", "p-me"), "p-me")).toBe(true);
    expect(isOwnCollection(collection("a", "p-parent"), "p-me")).toBe(false);
    expect(isOwnCollection(collection("a", "p-me"), null)).toBe(false);
  });

  it("splits own collections from shared ones, grouped by owner in profile-list order", () => {
    const listed = [
      collection("mine-1", "p-me"),
      collection("mine-2", "p-me"),
      collection("parent-1", "p-parent"),
      collection("kid-1", "p-kid"),
      collection("parent-2", "p-parent"),
      collection("gone-1", "p-gone"),
    ];
    const { own, shared } = partitionPersonalCollections(listed, "p-me", profiles);
    expect(own.map((c) => c.id)).toEqual(["mine-1", "mine-2"]);
    expect(shared.map((group) => [group.owner, group.collections.map((c) => c.id)])).toEqual([
      [{ id: "p-kid", name: "Kid" }, ["kid-1"]],
      [{ id: "p-parent", name: "Parent" }, ["parent-1", "parent-2"]],
      // An owner missing from the profile list goes last.
      [{ id: "p-gone", name: "Another profile" }, ["gone-1"]],
    ]);
  });
});
