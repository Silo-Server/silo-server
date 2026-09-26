import type { components } from "@/api/v2/schema";
import type {
  CreateLibraryCollectionRequest,
  UpdateLibraryCollectionRequest,
  LibraryCollection,
  LibraryCollectionGroup,
} from "@/api/types";
import { normalizeQueryDefinition } from "@/api/types";
import { v2, V2ProblemError } from "@/api/v2/request";
import { requiredETag } from "@/api/personalCollections";

type AdminCollection = components["schemas"]["AdminCollection"];
export function adminCollectionFromV2(value: AdminCollection): LibraryCollection {
  return {
    ...value,
    library_id: Number(value.library_id),
    library_ids: value.library_ids.map(Number),
    collection_type: value.collection_type as LibraryCollection["collection_type"],
    visibility: value.visibility as LibraryCollection["visibility"],
    management_mode: value.management_mode as LibraryCollection["management_mode"],
    last_sync_status: value.last_sync_status as LibraryCollection["last_sync_status"],
    query_definition: normalizeQueryDefinition(
      value.query_definition as LibraryCollection["query_definition"],
    ),
    source_config: value.source_config as Record<string, unknown>,
    sort_config: value.sort_config as Record<string, unknown>,
  };
}
export function adminGroupFromV2(
  value: components["schemas"]["AdminCollectionGroup"],
): LibraryCollectionGroup {
  return {
    ...value,
    library_id: Number(value.library_id),
    kind: value.kind as LibraryCollectionGroup["kind"],
    default_sort_mode: value.default_sort_mode as LibraryCollectionGroup["default_sort_mode"],
  };
}
export async function fetchAdminCollections(libraryId?: number) {
  const data = await v2("GET /api/v2/admin/collections", {
    query: { library_id: libraryId ? String(libraryId) : undefined },
  });
  return {
    collections: data.items.map(adminCollectionFromV2),
    groups: data.groups.map(adminGroupFromV2),
  };
}
export async function fetchAdminGroups(libraryId: number) {
  const data = await v2("GET /api/v2/admin/libraries/{library_id}/collection-groups", {
    path: { library_id: String(libraryId) },
  });
  return {
    groups: data.items.map(adminGroupFromV2),
    ungrouped_sort_order: data.ungrouped_sort_order,
  };
}
export function adminCreateBody(body: CreateLibraryCollectionRequest) {
  const {
    poster_source_url: _poster,
    backdrop_source_url: _backdrop,
    library_id,
    library_ids,
    ...fields
  } = body;
  return {
    ...fields,
    library_id: library_id === undefined ? undefined : String(library_id),
    library_ids: library_ids?.map(String),
  };
}
export function adminUpdateBody(body: UpdateLibraryCollectionRequest & { library_id?: number }) {
  const {
    poster_source_url: _poster,
    backdrop_source_url: _backdrop,
    library_id: _library,
    library_ids,
    ...fields
  } = body;
  return { ...fields, library_ids: library_ids?.map(String) };
}
export async function saveAdminArtwork(
  value: AdminCollection,
  body: { poster_source_url?: string; backdrop_source_url?: string },
  poster?: File | null,
  backdrop?: File | null,
  removeArtwork: ("poster" | "backdrop")[] = [],
) {
  let collection = value;
  const artworkErrors: string[] = [];
  for (const type of ["poster", "backdrop"] as const) {
    const file = type === "poster" ? poster : backdrop;
    const source = body[`${type}_source_url`];
    if (!file && !source && !removeArtwork.includes(type)) continue;
    try {
      if (!file && !source) {
        await v2("DELETE /api/v2/admin/collections/{id}/image", {
          path: { id: value.id },
          query: { type },
        });
        collection = { ...collection, [`${type}_url`]: "", [`${type}_thumbhash`]: undefined };
        continue;
      }
      collection = await v2(
        type === "poster"
          ? "PUT /api/v2/admin/collections/{id}/poster"
          : "PUT /api/v2/admin/collections/{id}/backdrop",
        {
          path: { id: value.id },
          form: file ? { image: file } : { source_url: source! },
        },
      );
    } catch (error) {
      artworkErrors.push(
        `${type}: ${error instanceof Error ? error.message : "Artwork update failed"}`,
      );
    }
  }
  return { collection: adminCollectionFromV2(collection), artworkErrors };
}
export function adminMutationMessage(error: unknown, fallback: string) {
  return error instanceof V2ProblemError && error.status === 412
    ? "This collection changed. Reload and review your changes before trying again."
    : error instanceof Error
      ? error.message
      : fallback;
}
export async function fetchAdminCollectionSnapshot(id: string) {
  let etag: string | null = null;
  const collection = await v2("GET /api/v2/admin/collections/{id}", {
    path: { id },
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { collection: adminCollectionFromV2(collection), etag: requiredETag(etag) };
}
export async function fetchAdminGroupSnapshot(id: string) {
  let etag: string | null = null;
  const group = await v2("GET /api/v2/admin/collection-groups/{id}", {
    path: { id },
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { group: adminGroupFromV2(group), etag: requiredETag(etag) };
}
export async function fetchAdminGroupOrderSnapshot(libraryId: number) {
  let etag: string | null = null;
  const body = await v2("GET /api/v2/admin/libraries/{library_id}/collection-groups/order", {
    path: { library_id: String(libraryId) },
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { ...body, etag: requiredETag(etag) };
}
export async function fetchAdminGroupCollectionOrderSnapshot(groupID: string, libraryId: number) {
  let etag: string | null = null;
  const body = await v2("GET /api/v2/admin/collection-groups/{group_id}/collections/order", {
    path: { group_id: groupID },
    query: { library_id: String(libraryId) },
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { ...body, etag: requiredETag(etag) };
}
export async function fetchAdminItemOrderSnapshot(id: string) {
  let etag: string | null = null;
  const body = await v2("GET /api/v2/admin/collections/{id}/items/order", {
    path: { id },
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { ...body, etag: requiredETag(etag) };
}
/** Read before opening the confirmation, with at most four requests in flight. */
export async function prepareAdminCollectionDeletes(ids: string[]) {
  const snapshots: { id: string; etag: string }[] = [];
  const unique = [...new Set(ids)];
  for (let offset = 0; offset < unique.length; offset += 4) {
    snapshots.push(
      ...(await Promise.all(
        unique
          .slice(offset, offset + 4)
          .map(async (id) => ({ id, etag: (await fetchAdminCollectionSnapshot(id)).etag })),
      )),
    );
  }
  return snapshots;
}
export function adminImportBody<
  T extends {
    library_id?: number;
    library_ids?: number[];
    poster_source_url?: string;
    backdrop_source_url?: string;
  },
>(body: T) {
  const {
    library_id,
    library_ids,
    poster_source_url: _poster,
    backdrop_source_url: _backdrop,
    ...fields
  } = body;
  return {
    ...fields,
    library_id: library_id === undefined ? undefined : String(library_id),
    library_ids: library_ids?.map(String),
  };
}
export function templateApplyBody(
  body: import("@/lib/collectionTemplates").ApplyCollectionTemplateBundleRequest,
) {
  return {
    ...body,
    library_ids: body.library_ids.map(String),
    featured: body.featured
      ? {
          ...body.featured,
          home: body.featured.home
            ? { ...body.featured.home, library_id: String(body.featured.home.library_id) }
            : undefined,
        }
      : undefined,
  };
}
export function templateResultFromV2(
  result: components["schemas"]["AdminTemplateResult"],
): import("@/lib/collectionTemplates").ApplyCollectionTemplateBundleResponse {
  return {
    ...result,
    created: result.created.map((entry) => ({ ...entry, library_id: Number(entry.library_id) })),
    skipped: result.skipped.map((entry) => ({ ...entry, library_id: Number(entry.library_id) })),
    failed: result.failed.map((entry) => ({ ...entry, library_id: Number(entry.library_id) })),
    sync_queued: result.sync_queued.map((entry) => ({
      ...entry,
      library_id: Number(entry.library_id),
    })),
    deleted: result.deleted.map((entry) => ({ ...entry, library_id: Number(entry.library_id) })),
    delete_skipped: result.delete_skipped.map((entry) => ({
      ...entry,
      library_id: Number(entry.library_id),
    })),
    delete_failed: result.delete_failed.map((entry) => ({
      ...entry,
      library_id: Number(entry.library_id),
    })),
    featured: result.featured.map((entry) => ({
      ...entry,
      library_id: entry.library_id ? Number(entry.library_id) : undefined,
    })),
    featured_failed: result.featured_failed.map((entry) => ({
      ...entry,
      library_id: entry.library_id ? Number(entry.library_id) : undefined,
    })),
  };
}
/** Group mutations share a library revision. Bracket the reads so a move between
 * source and destination reads cannot produce a mixed snapshot. */
export async function fetchAdminBoardOrderSnapshot(libraryId: number, groupIds: string[]) {
  const groupOrder = await fetchAdminGroupOrderSnapshot(libraryId);
  const collectionOrders = new Map<
    string,
    Awaited<ReturnType<typeof fetchAdminGroupCollectionOrderSnapshot>>
  >();
  const ids = [...groupIds, "ungrouped"];
  for (let offset = 0; offset < ids.length; offset += 4) {
    const batch = await Promise.all(
      ids
        .slice(offset, offset + 4)
        .map(
          async (id) => [id, await fetchAdminGroupCollectionOrderSnapshot(id, libraryId)] as const,
        ),
    );
    for (const [id, snapshot] of batch) collectionOrders.set(id, snapshot);
  }
  if ((await fetchAdminGroupOrderSnapshot(libraryId)).etag !== groupOrder.etag)
    throw new Error("Collection order changed while loading. Reload before reordering.");
  return { groupOrder, collectionOrders };
}
