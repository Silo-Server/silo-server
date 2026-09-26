import type {
  Collection,
  CollectionGroup,
  CollectionsListResponse,
  CreateCollectionRequest,
  UpdateCollectionRequest,
  CollectionPreviewRequest,
  CollectionPreviewResponse,
  MDBListDiscoveryResponse,
  UserImportSharedFields,
  ImportUserCollectionResponse,
  UserCollectionSyncResult,
  ServerCollectionsResponse,
} from "@/api/types";
import { normalizeQueryDefinition } from "@/api/types";
import { v2, type V2Body, type V2Result } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";

type CollectionV2 = components["schemas"]["PersonalCollection"];

export function collectionFromV2(value: CollectionV2): Collection {
  return {
    ...value,
    collection_type: value.collection_type as Collection["collection_type"],
    query_definition: normalizeQueryDefinition(
      value.query_definition as Collection["query_definition"],
    ),
    sort_config: value.sort_config as Record<string, unknown>,
    source_config: value.source_config as Record<string, unknown> | undefined,
    display_query_definition:
      value.display_query_definition as Collection["display_query_definition"],
    next_sync_at: value.next_sync_at ?? undefined,
    last_sync_at: value.last_sync_at ?? undefined,
    last_sync_status: value.last_sync_status as Collection["last_sync_status"],
  };
}

export function collectionsFromV2(
  value: V2Result<"GET /api/v2/collections">,
): CollectionsListResponse {
  return {
    collections: value.items.map(collectionFromV2),
    groups: value.groups.map((group) => ({
      ...group,
      default_sort_mode: group.default_sort_mode as CollectionGroup["default_sort_mode"],
    })),
  };
}

export function collectionCreateToV2(
  body: CreateCollectionRequest,
): V2Body<"POST /api/v2/collections"> {
  return { ...body };
}

export function collectionUpdateToV2(
  body: UpdateCollectionRequest,
): V2Body<"PATCH /api/v2/collections/{id}"> {
  const { poster_source_url: _posterSource, ...fields } = body;
  return { ...fields, library_ids: body.library_ids?.map(String) };
}

/** Saving succeeded even when a later poster upload fails. Return the saved
 * resource so the dialog closes and retries cannot create duplicate collections. */
export async function saveCollectionPoster(
  collection: CollectionV2,
  poster?: File | null,
  sourceURL?: string,
) {
  if (!poster && !sourceURL)
    return { collection: collectionFromV2(collection), posterError: undefined };
  try {
    const updated = await v2("PUT /api/v2/collections/{id}/poster", {
      path: { id: collection.id },
      form: poster ? { poster } : { source_url: sourceURL! },
    });
    return { collection: collectionFromV2(updated), posterError: undefined };
  } catch (error) {
    return {
      collection: collectionFromV2(collection),
      posterError: error instanceof Error ? error.message : "Poster upload failed",
    };
  }
}

export function previewToV2(
  body: CollectionPreviewRequest,
): V2Body<"POST /api/v2/collections/preview"> {
  return { query_definition: body.query_definition, limit: body.limit ?? 12 };
}
export function previewFromV2(
  value: V2Result<"POST /api/v2/collections/preview">,
): CollectionPreviewResponse {
  return { items: value.items, total: value.total };
}
export function discoveryFromV2(
  value: V2Result<"GET /api/v2/collections/import/mdblist/top">,
): MDBListDiscoveryResponse {
  return {
    configured: value.configured,
    lists: value.items.map(({ id, user_id, media_type, ...rest }) => ({
      ...rest,
      id: Number(id),
      user_id: Number(user_id),
      mediatype: media_type,
    })),
  };
}
export function importBodyToV2<T extends UserImportSharedFields>(body: T) {
  return { ...body, library_ids: body.library_ids?.map(String) };
}
export function syncFromV2(
  value: components["schemas"]["CollectionSyncResult"],
): UserCollectionSyncResult {
  return { ...value, status: value.status as UserCollectionSyncResult["status"] };
}
export function importFromV2(
  value: components["schemas"]["CollectionImportResult"],
): ImportUserCollectionResponse {
  return {
    collection: collectionFromV2(value.collection),
    sync: value.sync ? syncFromV2(value.sync) : undefined,
  };
}
export function serverCollectionsFromV2(
  value: V2Result<"GET /api/v2/collections/server">,
): ServerCollectionsResponse["libraries"] {
  return value.libraries.map((library) => ({
    ...library,
    library_id: Number(library.library_id),
    collections: library.collections,
  }));
}

export interface CollectionEditSnapshot {
  collection: Collection;
  etag: string;
}
export function requiredETag(etag: string | undefined | null): string {
  if (!etag || etag === "*")
    throw new Error("Reload this collection before editing; its version is unavailable.");
  return etag;
}
export async function fetchCollectionEditSnapshot(id: string): Promise<CollectionEditSnapshot> {
  let etag: string | null = null;
  const collection = await v2("GET /api/v2/collections/{id}", {
    path: { id },
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { collection: collectionFromV2(collection), etag: requiredETag(etag) };
}

export async function fetchCollectionOrderSnapshot(groupID: string | null) {
  let etag: string | null = null;
  const body = await v2("GET /api/v2/collections/order", {
    query: groupID ? { group_id: groupID } : {},
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { ...body, etag: requiredETag(etag) };
}
export async function fetchGroupOrderSnapshot() {
  let etag: string | null = null;
  const body = await v2("GET /api/v2/collections/groups/order", {
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { ...body, etag: requiredETag(etag) };
}
export async function fetchGroupSnapshot(id: string) {
  let etag: string | null = null;
  const group = await v2("GET /api/v2/collections/groups/{id}", {
    path: { id },
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { group, etag: requiredETag(etag) };
}
export async function fetchItemOrderSnapshot(id: string) {
  let etag: string | null = null;
  const body = await v2("GET /api/v2/collections/{id}/items/order", {
    path: { id },
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { ...body, etag: requiredETag(etag) };
}
