// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { RestoreItem } from "@/api/v2/adminBackupRestore";

import { countStates, groupSectionItems } from "./restoreGroups";

function item(overrides: Partial<RestoreItem> & Pick<RestoreItem, "id">): RestoreItem {
  return {
    kind: "collection",
    label: overrides.id,
    status: "on_this_server",
    reason: "same",
    selectable: false,
    requires: [],
    restart_required: false,
    external_service: false,
    missing_blobs: [],
    ...overrides,
  };
}

const differs = (id: string) =>
  item({ id, status: "differs", reason: undefined, selectable: true });
const fresh = (id: string) =>
  item({ id, status: "not_on_this_server", reason: undefined, selectable: true });
const blocked = (id: string, reason: RestoreItem["reason"]) =>
  item({ id, status: "not_on_this_server", reason, selectable: false });

describe("countStates", () => {
  it("counts each state for the difference bar", () => {
    expect(
      countStates([
        item({ id: "a" }),
        item({ id: "b" }),
        differs("c"),
        fresh("d"),
        blocked("e", "folder_missing"),
      ]),
    ).toEqual({ same: 2, differs: 1, new: 1, blocked: 1 });
  });
});

describe("groupSectionItems", () => {
  it("splits restorable, blocked by reason (largest first), and same", () => {
    const groups = groupSectionItems([
      differs("a"),
      item({ id: "b" }),
      blocked("c", "library_not_mapped"),
      blocked("d", "folder_missing"),
      blocked("e", "folder_missing"),
      fresh("f"),
      item({ id: "g", status: "ambiguous", reason: undefined }),
    ]);
    expect(groups.restorable.map((entry) => entry.id)).toEqual(["a", "f"]);
    expect(groups.same.map((entry) => entry.id)).toEqual(["b"]);
    expect(groups.blocked.map((group) => [group.key, group.items.length])).toEqual([
      ["folder_missing", 2],
      ["library_not_mapped", 1],
      ["status:ambiguous", 1],
    ]);
  });
});
