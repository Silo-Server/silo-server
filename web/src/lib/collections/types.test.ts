import { describe, expect, it } from "vitest";

import {
  collectionKindOf,
  collectionTypeLabel,
  isListBackedCollectionType,
  syncedSourceOf,
} from "./types";

// Every collection_type the server stores, and every TMDB source mode
// (internal/catalog: tmdb_preset, tmdb_list, tmdb_collection, tmdb_discover).
const stored: Array<[string, Record<string, unknown> | undefined, string]> = [
  ["manual", undefined, "Manual"],
  ["smart", undefined, "Smart"],
  ["mdblist", { mode: "mdblist_json" }, "Synced list · MDBList"],
  ["trakt", { mode: "trakt_preset" }, "Synced list · Trakt (legacy)"],
  ["tmdb", undefined, "Synced list · TMDB chart"],
  ["tmdb", { mode: "" }, "Synced list · TMDB chart"],
  ["tmdb", { mode: "tmdb_preset" }, "Synced list · TMDB chart"],
  ["tmdb", { mode: "tmdb_list" }, "Synced list · TMDB list"],
  ["tmdb", { mode: "tmdb_collection" }, "Synced list · TMDB franchise"],
  ["tmdb", { mode: "tmdb_discover" }, "Synced list · TMDB Discover"],
];

describe("collection type labels", () => {
  it.each(stored)("labels %s with mode %j as %s", (type, sourceConfig, label) => {
    expect(collectionTypeLabel({ collection_type: type, source_config: sourceConfig })).toBe(label);
  });

  it("gives every stored type exactly one kind, and a source only to synced lists", () => {
    for (const [type, sourceConfig] of stored) {
      const kind = collectionKindOf(type);
      const source = syncedSourceOf(type, sourceConfig);
      expect(kind === "synced").toBe(source !== null);
      expect(kind === "synced").toBe(
        isListBackedCollectionType(type as Parameters<typeof isListBackedCollectionType>[0]),
      );
    }
  });

  it("calls an unknown stored type a synced list without naming a source", () => {
    expect(collectionKindOf("letterboxd")).toBe("synced");
    expect(syncedSourceOf("letterboxd")).toBeNull();
    expect(collectionTypeLabel({ collection_type: "letterboxd" })).toBe("Synced list");
  });
});
