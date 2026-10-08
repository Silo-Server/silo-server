import type { LibraryCollection } from "@/api/types";

// List-backed collections follow an MDBList, TMDB or legacy Trakt list, and
// only they can be synced: a manual collection has no list, and a smart one
// is filled from its rules whenever it is read. Mirrors catalog.IsSyncableType
// in internal/catalog/collection_type.go.
export function isListBackedCollectionType(
  collectionType: LibraryCollection["collection_type"],
): boolean {
  return collectionType === "mdblist" || collectionType === "tmdb" || collectionType === "trakt";
}

/** What a person picks when they make a collection; every stored list type is "synced". */
export type CollectionKind = "manual" | "smart" | "synced";

/** Where a synced list comes from. TMDB splits by the stored source mode. */
export type SyncedSource =
  | "mdblist"
  | "tmdb_chart"
  | "tmdb_list"
  | "tmdb_franchise"
  | "tmdb_discover"
  | "trakt";

export const COLLECTION_KIND_LABEL: Readonly<Record<CollectionKind, string>> = {
  manual: "Manual",
  smart: "Smart",
  synced: "Synced list",
};

export const SYNCED_SOURCE_LABEL: Readonly<Record<SyncedSource, string>> = {
  mdblist: "MDBList",
  tmdb_chart: "TMDB chart",
  tmdb_list: "TMDB list",
  tmdb_franchise: "TMDB franchise",
  tmdb_discover: "TMDB Discover",
  trakt: "Trakt (legacy)",
};

export function collectionKindOf(collectionType: string): CollectionKind {
  if (collectionType === "manual" || collectionType === "smart") return collectionType;
  return "synced";
}

/**
 * The list a synced collection follows, or null for manual and smart ones.
 * A TMDB collection's `source_config.mode` names its kind; a missing or empty
 * mode is a chart preset, as the server stores older imports.
 */
export function syncedSourceOf(
  collectionType: string,
  sourceConfig?: Record<string, unknown> | null,
): SyncedSource | null {
  switch (collectionType) {
    case "mdblist":
      return "mdblist";
    case "trakt":
      return "trakt";
    case "tmdb":
      switch (sourceConfig?.mode) {
        case "tmdb_list":
          return "tmdb_list";
        case "tmdb_collection":
          return "tmdb_franchise";
        case "tmdb_discover":
          return "tmdb_discover";
        default:
          return "tmdb_chart";
      }
    default:
      return null;
  }
}

/** "Manual", "Smart", or "Synced list · MDBList". */
export function collectionTypeLabel(collection: {
  collection_type: string;
  source_config?: Record<string, unknown> | null;
}): string {
  const kind = collectionKindOf(collection.collection_type);
  const source = syncedSourceOf(collection.collection_type, collection.source_config);
  return source
    ? `${COLLECTION_KIND_LABEL[kind]} · ${SYNCED_SOURCE_LABEL[source]}`
    : COLLECTION_KIND_LABEL[kind];
}
