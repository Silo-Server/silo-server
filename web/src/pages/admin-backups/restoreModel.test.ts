// @vitest-environment node

import { describe, expect, it } from "vitest";

import type { RestoreItem, RestoreSet } from "@/api/v2/adminBackupRestore";

import { chooseItems, indexItems, selectionRequest, unchooseItems } from "./restoreModel";

function item(overrides: Partial<RestoreItem> & Pick<RestoreItem, "id" | "kind">): RestoreItem {
  return {
    label: overrides.id,
    status: "not_on_this_server",
    selectable: true,
    requires: [],
    restart_required: false,
    external_service: false,
    missing_blobs: [],
    ...overrides,
  };
}

const SET = {
  backup_id: "b",
  digest: "d",
  format_version: 1,
  server_id: "s",
  server_name: "Home",
  silo_version: "1.0.0",
  created_at: "2026-10-31T04:00:00Z",
  same_server: true,
  key_matches: true,
  missing_blobs: [],
  library_map: [],
  never_restored_settings: [],
  sections: [
    {
      id: "libraries",
      count: 3,
      items: [
        item({ id: "1", kind: "library", label: "Kids" }),
        item({
          id: "2",
          kind: "library",
          label: "Anime",
          status: "folder_missing",
          selectable: false,
        }),
        item({
          id: "3",
          kind: "library",
          label: "Movies",
          status: "on_this_server",
          selectable: false,
        }),
      ],
    },
    {
      id: "collections",
      count: 3,
      items: [
        item({
          id: "collection:9",
          kind: "collection",
          label: "Cartoons",
          requires: ["libraries:1"],
          collection: { items: 12, missing_items: 12, unknown_items: 2 },
        }),
        item({
          id: "collection:10",
          kind: "collection",
          label: "Shows",
          requires: ["libraries:2"],
          collection: { items: 4, missing_items: 4, unknown_items: 0 },
        }),
        item({
          id: "collection:11",
          kind: "collection",
          label: "Favourites",
          status: "differs",
          collection: { items: 20, missing_items: 3, unknown_items: 0, local_id: "7" },
        }),
      ],
    },
  ],
} satisfies RestoreSet;

const index = indexItems(SET);

describe("choosing items", () => {
  it("chooses the library a collection needs", () => {
    const change = chooseItems(index, new Set(), ["collections:collection:9"]);
    expect([...change.selection].sort()).toEqual(["collections:collection:9", "libraries:1"]);
  });

  it("refuses a collection whose library can't be restored", () => {
    const change = chooseItems(index, new Set(), ["collections:collection:10"]);
    expect(change.selection.size).toBe(0);
  });

  it("unchooses collections with the library they need", () => {
    const chosen = chooseItems(index, new Set(), [
      "collections:collection:9",
      "collections:collection:11",
    ]).selection;
    const change = unchooseItems(index, chosen, ["libraries:1"]);
    expect([...change.selection]).toEqual(["collections:collection:11"]);
  });

  it("sends item ids per section", () => {
    const chosen = chooseItems(index, new Set(), ["collections:collection:9"]).selection;
    expect(selectionRequest(SET, chosen)).toEqual({
      libraries: ["1"],
      plugins: [],
      settings: [],
      collections: ["collection:9"],
      home_sections: [],
    });
  });
});
