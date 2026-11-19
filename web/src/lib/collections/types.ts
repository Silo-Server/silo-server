import type { LibraryCollection } from "@/api/types";
import { t, type MessageKey } from "@/i18n";

// List-backed collections follow an MDBList or TMDB list, and
// only they can be synced: a manual collection has no list, and a smart one
// is filled from its rules whenever it is read. Mirrors catalog.IsSyncableType
// in internal/catalog/collection_type.go.
export function isListBackedCollectionType(
  collectionType: LibraryCollection["collection_type"],
): boolean {
  return collectionType === "mdblist" || collectionType === "tmdb";
}

/** What a person picks when they make a collection; every stored list type is "synced". */
export type CollectionKind = "manual" | "smart" | "synced";

/** Where a synced list comes from. TMDB splits by the stored source mode. */
export type SyncedSource =
  | "mdblist"
  | "tmdb_chart"
  | "tmdb_list"
  | "tmdb_franchise"
  | "tmdb_discover";

const COLLECTION_KIND_LABEL: Readonly<Record<CollectionKind, MessageKey>> = {
  manual: "web.collections.types.kindManual",
  smart: "web.collections.types.kindSmart",
  synced: "web.collections.types.kindSynced",
};

const SYNCED_SOURCE_LABEL: Readonly<Record<SyncedSource, MessageKey>> = {
  mdblist: "web.collections.types.sourceMdblist",
  tmdb_chart: "web.collections.types.sourceTmdbChart",
  tmdb_list: "web.collections.types.sourceTmdbList",
  tmdb_franchise: "web.collections.types.sourceTmdbFranchise",
  tmdb_discover: "web.collections.types.sourceTmdbDiscover",
};

/** "Manual", "Smart", "Synced list". */
export function collectionKindLabel(kind: CollectionKind): string {
  return t(COLLECTION_KIND_LABEL[kind]);
}

/** "MDBList", "TMDB chart": where a synced list comes from. */
export function syncedSourceLabel(source: SyncedSource): string {
  return t(SYNCED_SOURCE_LABEL[source]);
}

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
    ? `${collectionKindLabel(kind)} · ${syncedSourceLabel(source)}`
    : collectionKindLabel(kind);
}
