/**
 * Server answers the collection goldens need that `contracts/api/v2/fixtures`
 * does not record. Each follows its operation's response schema.
 */
import getAdminCollectionOk from "../../../../contracts/api/v2/fixtures/get_admin_collection_ok.json";
import getCollectionOk from "../../../../contracts/api/v2/fixtures/get_collection_ok.json";

/** `GET /api/v2/admin/collections/capabilities` with every feature on. */
export const adminCapabilities = {
  revision: "1",
  state: "available",
  allowed: true,
  groups: true,
  imports: true,
  import_sources: ["mdblist", "tmdb", "tmdb_list"],
  mdblist_search: true,
  schedule_time_zone: { utc_offset: "-05:00", abbreviation: "CDT" },
  artwork: true,
  item_reorder: true,
};

/** `GET /api/v2/collections/capabilities` with every feature on. */
export const personalCapabilities = {
  revision: "1",
  state: "available",
  allowed: true,
  groups: false,
  login_sharing: true,
  imports: true,
  import_sources: ["mdblist", "tmdb", "tmdb_list"],
  mdblist_search: true,
  schedule_time_zone: { utc_offset: "-05:00", abbreviation: "CDT" },
  artwork: true,
  item_reorder: true,
  display_filter_fields: [],
  display_filter_presets: {
    watched: ["all", "watched", "unwatched"],
    media: ["all", "movie", "series"],
  },
  collection_default_sort: true,
  collection_sort_preferences: true,
  effective_collection_sort: true,
  sort_preference_kinds: [],
  sync_schedule_editable: true,
  poster_collages: true,
};

/**
 * The admin collection fixture as the server stores a collection made in the
 * editor: the fixture leaves `visibility` blank.
 */
export const adminCollection = { ...getAdminCollectionOk, visibility: "visible" };

/** An empty preview page, personal or admin. */
export const emptyPreview = { items: [], page: { has_more: false }, total: 0 };

/** The admin collection fixture, as a smart collection of `queryDefinition`. */
export function adminSmartCollection(queryDefinition: Record<string, unknown>) {
  return { ...adminCollection, collection_type: "smart", query_definition: queryDefinition };
}

/** The personal collection fixture, as a smart collection of `queryDefinition`. */
export function personalSmartCollection(queryDefinition: Record<string, unknown>) {
  return { ...getCollectionOk, collection_type: "smart", query_definition: queryDefinition };
}

/** The admin collection fixture as a synced collection of `type` with `source`. */
export function adminSyncedCollection(
  type: "mdblist" | "tmdb" | "trakt",
  source: { source_url: string; source_config: Record<string, unknown> },
) {
  return { ...adminCollection, collection_type: type, ...source };
}

/** The personal collection fixture as a synced collection of `type` with `source`. */
export function personalSyncedCollection(
  type: "mdblist" | "tmdb" | "trakt",
  source: { source_url: string; source_config: Record<string, unknown> },
) {
  return { ...getCollectionOk, collection_type: type, ...source };
}

/** `GET /api/v2/admin/collections`, listing the given collections. */
export function adminCollectionList(...items: unknown[]) {
  return { items, groups: [], page: { has_more: false } };
}
