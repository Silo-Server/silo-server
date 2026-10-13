import type {
  Collection,
  CollectionsListResponse,
  CreateCollectionRequest,
  UpdateCollectionRequest,
  CollectionPreviewRequest,
  CollectionPreviewResponse,
  MDBListDiscoveryResponse,
  UserImportSharedFields,
  UserCollectionSyncResult,
  ServerCollectionsResponse,
} from "@/api/types";
import { normalizeQueryDefinition } from "@/api/types";
import { withETag } from "@/api/v2/etag";
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
  return { collections: value.items.map(collectionFromV2) };
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

/** Saving succeeded even when a later poster change fails. Return the saved
 * resource so the dialog closes and retries cannot create duplicate collections.
 *
 * A removal staged in the editor is sent here, after the collection saved. A
 * new file or URL wins over it: the upload replaces the poster and clears the
 * old image itself, so a DELETE would only remove the new one. */
export async function saveCollectionPoster(
  collection: CollectionV2,
  poster?: File | null,
  sourceURL?: string,
  removePoster = false,
) {
  if (!poster && !sourceURL && !removePoster)
    return { collection: collectionFromV2(collection), posterError: undefined };
  try {
    if (!poster && !sourceURL) {
      await v2("DELETE /api/v2/collections/{id}/image", {
        path: { id: collection.id },
        query: { type: "poster" },
      });
      return {
        collection: collectionFromV2({ ...collection, poster_url: "", poster_thumbhash: "" }),
        posterError: undefined,
      };
    }
    const updated = await v2("PUT /api/v2/collections/{id}/poster", {
      path: { id: collection.id },
      form: poster ? { poster } : { source_url: sourceURL! },
    });
    return { collection: collectionFromV2(updated), posterError: undefined };
  } catch (error) {
    return {
      collection: collectionFromV2(collection),
      posterError: error instanceof Error ? error.message : "Poster update failed",
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
export async function fetchCollectionEditSnapshot(id: string): Promise<CollectionEditSnapshot> {
  const { body, etag } = await withETag("GET /api/v2/collections/{id}", { path: { id } });
  return { collection: collectionFromV2(body), etag };
}

/** The acting profile's own collections in its order, with the order's validator. */
export async function fetchCollectionOrderSnapshot() {
  const { body, etag } = await withETag("GET /api/v2/collections/order");
  return { ordered_ids: body.ordered_ids, etag };
}
export async function fetchItemOrderSnapshot(id: string) {
  const { body, etag } = await withETag("GET /api/v2/collections/{id}/items/order", {
    path: { id },
  });
  return { ...body, etag };
}
