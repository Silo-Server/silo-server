import { fetchAdminItemOrderSnapshot } from "@/api/adminCollections";
import { useCallback } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  isCapturedProfileAuthorityActive,
  isProfileRequestContextCurrent,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type {
  Collection,
  CollectionCapabilitiesResponse,
  CollectionGroup,
  CollectionSortConfig,
  CollectionsListResponse,
  CreateCollectionRequest,
  UpdateCollectionRequest,
} from "@/api/types";
import { v2, V2ProblemError } from "@/api/v2/request";
import {
  fetchCollectionEditSnapshot,
  fetchItemOrderSnapshot,
  requiredETag,
  collectionsFromV2,
  collectionCreateToV2,
  collectionUpdateToV2,
  saveCollectionPoster,
  serverCollectionsFromV2,
} from "@/api/personalCollections";
import { catalogKeys, collectionKeys } from "./keys";
import { toast } from "sonner";
import {
  invalidateUserCollectionQueries,
  invalidateAdminCollectionQueries,
} from "./collectionSurfaceRefresh";

// Single fetcher for /collections — both useCollections and useCollectionGroups
// share the cache so the page makes one network round-trip.
function fetchCollectionsList(): Promise<CollectionsListResponse> {
  return v2("GET /api/v2/collections").then(collectionsFromV2);
}

export function useCollectionEditSnapshot(id?: string) {
  return useQuery({
    queryKey: ["collections", "edit", id],
    queryFn: () => fetchCollectionEditSnapshot(id!),
    enabled: !!id,
  });
}

function collectionMutationMessage(error: unknown, fallback: string) {
  if (error instanceof V2ProblemError && error.status === 412) {
    return "This collection changed while you were editing. Reload it and review your changes before saving again.";
  }
  return error instanceof Error ? error.message : fallback;
}
export function useCollections() {
  return useQuery({
    queryKey: collectionKeys.list(),
    queryFn: fetchCollectionsList,
    select: (data) => data.collections,
  });
}

export function useCollectionGroups() {
  return useQuery({
    queryKey: collectionKeys.list(),
    queryFn: fetchCollectionsList,
    select: (data) => data.groups,
  });
}

export function useCollectionCapabilities() {
  return useQuery({
    queryKey: ["collections", "capabilities"],
    queryFn: () =>
      v2("GET /api/v2/collections/capabilities").then((value) => ({
        ...value,
        display_filter_presets:
          value.display_filter_presets as CollectionCapabilitiesResponse["display_filter_presets"],
      })),
    staleTime: Number.POSITIVE_INFINITY,
  });
}

// useServerCollections loads the admin-curated "server" collections aggregated
// across every library the viewer can access. Kept on a separate query key from
// useCollections() (personal, editable) so personal mutations don't refetch the
// server-wide catalog and the two sections load independently.
export function useServerCollections() {
  return useQuery({
    queryKey: collectionKeys.server(),
    queryFn: () => v2("GET /api/v2/collections/server").then(serverCollectionsFromV2),
  });
}

export function useCollectionItems(
  collectionId: string,
  cursor = "",
  source: "user" | "library" = "user",
) {
  return useQuery({
    queryKey:
      source === "user"
        ? [...collectionKeys.items(collectionId), "page", cursor]
        : ["libraryCollections", "items", collectionId, "page", cursor],
    queryFn: () =>
      v2(
        source === "user"
          ? "GET /api/v2/collections/{id}/items"
          : "GET /api/v2/admin/collections/{id}/items",
        {
          path: { id: collectionId },
          query: { limit: 200, ...(cursor ? { cursor } : {}) },
        },
      ),
    // Keep only the visible edit window; old pages are inexpensive to refetch.
    gcTime: 0,
  });
}

export function useCollectionItemOrderSnapshot(
  id: string,
  enabled = true,
  source: "user" | "library" = "user",
) {
  return useQuery({
    queryKey: [source === "user" ? "collections" : "libraryCollections", "items", id, "order"],
    queryFn: () =>
      source === "user" ? fetchItemOrderSnapshot(id) : fetchAdminItemOrderSnapshot(id),
    enabled,
  });
}

export function useCreateCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ body, poster }: { body: CreateCollectionRequest; poster?: File | null }) =>
      v2("POST /api/v2/collections", { body: collectionCreateToV2(body) }).then((collection) =>
        saveCollectionPoster(collection, poster),
      ),
    onSuccess: ({ posterError }) => {
      toast.success("Collection created");
      if (posterError) toast.error(`Collection saved, but poster upload failed: ${posterError}`);
      return invalidateUserCollectionQueries(queryClient);
    },
    onError: (err) => {
      toast.error(collectionMutationMessage(err, "Failed to save"));
      if (err instanceof V2ProblemError && err.status === 412)
        void invalidateUserCollectionQueries(queryClient);
    },
  });
}

export function useUpdateCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      id,
      body,
      poster,
      etag,
    }: {
      id: string;
      etag: string;
      body: UpdateCollectionRequest;
      poster?: File | null;
    }) =>
      v2("PATCH /api/v2/collections/{id}", {
        path: { id },
        headers: { "If-Match": requiredETag(etag) },
        body: collectionUpdateToV2(body),
      }).then((collection) => saveCollectionPoster(collection, poster, body.poster_source_url)),
    onSuccess: ({ posterError }, { id }) => {
      toast.success("Collection updated");
      if (posterError) toast.error(`Collection saved, but poster upload failed: ${posterError}`);
      return invalidateUserCollectionQueries(queryClient, id);
    },
    onError: (err) => {
      toast.error(collectionMutationMessage(err, "Failed to save"));
      if (err instanceof V2ProblemError && err.status === 412)
        void invalidateUserCollectionQueries(queryClient);
    },
  });
}

export function useDeleteCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag }: { id: string; etag: string }) =>
      v2("DELETE /api/v2/collections/{id}", {
        path: { id },
        headers: { "If-Match": requiredETag(etag) },
      }),
    onSuccess: (_data, { id }) => {
      toast.success("Collection deleted");
      return invalidateUserCollectionQueries(queryClient, id);
    },
    onError: (err) => {
      toast.error(collectionMutationMessage(err, "Failed to delete"));
      if (err instanceof V2ProblemError && err.status === 412)
        void invalidateUserCollectionQueries(queryClient);
    },
  });
}

// useAddItemToCollection adds a single media item to either a personal user
// collection (PUT /collections/{id}/items/{itemId}) or an admin library
// collection (PUT /admin/collections/{id}/items/{itemId}). Source determines
// the route; manual collections are the only ones that meaningfully accept
// hand-curated items — synced collections will overwrite on the next sync.
export function useAddItemToCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      collectionId,
      mediaItemId,
      source,
      position,
    }: {
      collectionId: string;
      mediaItemId: string;
      source: "user" | "library";
      position?: number;
    }) => {
      if (source === "user")
        return v2("PUT /api/v2/collections/{id}/items/{item_id}", {
          path: { id: collectionId, item_id: mediaItemId },
          body: { position: position ?? 0 },
        });
      return v2("PUT /api/v2/admin/collections/{id}/items/{item_id}", {
        path: { id: collectionId, item_id: mediaItemId },
        body: { position: position ?? 0 },
      });
    },
    onSuccess: (_data, vars) => {
      toast.success("Added to collection");
      if (vars.source === "user") {
        return invalidateUserCollectionQueries(queryClient, vars.collectionId);
      }
      return invalidateAdminCollectionQueries(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to add to collection");
    },
  });
}

export function useRemoveCollectionItem(collectionId: string, source: "user" | "library" = "user") {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (mediaItemId: string) =>
      v2(
        source === "user"
          ? "DELETE /api/v2/collections/{id}/items/{item_id}"
          : "DELETE /api/v2/admin/collections/{id}/items/{item_id}",
        {
          path: { id: collectionId, item_id: mediaItemId },
        },
      ),
    onSuccess: () =>
      source === "user"
        ? invalidateUserCollectionQueries(queryClient, collectionId)
        : invalidateAdminCollectionQueries(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove item");
    },
  });
}

function reorderByIds<T>(items: T[], getId: (item: T) => string, orderedIds: string[]): T[] {
  const byId = new Map(items.map((item) => [getId(item), item]));
  const reordered: T[] = [];
  for (const id of orderedIds) {
    const item = byId.get(id);
    if (item) reordered.push(item);
  }
  return reordered;
}

export interface ReorderCollectionsArgs {
  orderedIds: string[];
  etag: string;
  groupId?: string | null;
}

export function useReorderCollections() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ orderedIds, groupId, etag }: ReorderCollectionsArgs) =>
      v2("PUT /api/v2/collections/order", {
        headers: { "If-Match": requiredETag(etag) },
        body: {
          ordered_ids: orderedIds,
          ...(groupId !== undefined ? { group_id: groupId } : {}),
        },
      }),
    onMutate: async ({ orderedIds, groupId }) => {
      await queryClient.cancelQueries({ queryKey: collectionKeys.list() });
      const snapshot = queryClient.getQueryData<CollectionsListResponse>(collectionKeys.list());
      if (snapshot) {
        const inScope = (c: Collection) =>
          groupId === undefined ? true : (c.group_id ?? null) === groupId;
        // Clone before stamping sort_order so the snapshot retained for
        // rollback (ctx.snapshot) keeps its original values when onError
        // restores the cache.
        const reordered = reorderByIds(
          snapshot.collections.filter(inScope),
          (c) => c.id,
          orderedIds,
        ).map((c, i) => ({ ...c, sort_order: i }));
        const next = [...snapshot.collections];
        let cursor = 0;
        for (let i = 0; i < next.length; i++) {
          const current = next[i];
          if (current && inScope(current)) {
            next[i] = reordered[cursor++] ?? current;
          }
        }
        queryClient.setQueryData<CollectionsListResponse>(collectionKeys.list(), {
          ...snapshot,
          collections: next,
        });
      }
      return { snapshot };
    },
    onError: (err, _vars, ctx) => {
      if (ctx?.snapshot) queryClient.setQueryData(collectionKeys.list(), ctx.snapshot);
      toast.error(collectionMutationMessage(err, "Failed to reorder"));
      if (err instanceof V2ProblemError && err.status === 412)
        void invalidateUserCollectionQueries(queryClient);
    },
    onSettled: () => invalidateUserCollectionQueries(queryClient),
  });
}

export function useCreateCollectionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ name, slug }: { name: string; slug?: string }) =>
      v2("POST /api/v2/collections/groups", {
        body: { name, slug },
      }),
    onSuccess: () => invalidateUserCollectionQueries(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to add group");
    },
  });
}

export function useUpdateCollectionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, name, etag }: { id: string; name: string; etag: string }) =>
      v2("PATCH /api/v2/collections/groups/{id}", {
        headers: { "If-Match": requiredETag(etag) },
        path: { id },
        body: { name },
      }),
    onSuccess: () => invalidateUserCollectionQueries(queryClient),
    onError: (err) => {
      toast.error(collectionMutationMessage(err, "Failed to rename group"));
      if (err instanceof V2ProblemError && err.status === 412)
        void invalidateUserCollectionQueries(queryClient);
    },
  });
}

export function useDeleteCollectionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag }: { id: string; etag: string }) =>
      v2("DELETE /api/v2/collections/groups/{id}", {
        path: { id },
        headers: { "If-Match": requiredETag(etag) },
      }),
    onSuccess: () => invalidateUserCollectionQueries(queryClient),
    onError: (err) => {
      toast.error(collectionMutationMessage(err, "Failed to delete group"));
      if (err instanceof V2ProblemError && err.status === 412)
        void invalidateUserCollectionQueries(queryClient);
    },
  });
}

export function useReorderCollectionGroups() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ orderedIds, etag }: { orderedIds: string[]; etag: string }) =>
      v2("PUT /api/v2/collections/groups/order", {
        headers: { "If-Match": requiredETag(etag) },
        body: { ordered_ids: orderedIds },
      }),
    onMutate: async ({ orderedIds }) => {
      await queryClient.cancelQueries({ queryKey: collectionKeys.list() });
      const snapshot = queryClient.getQueryData<CollectionsListResponse>(collectionKeys.list());
      if (snapshot) {
        const groups = reorderByIds(snapshot.groups, (g: CollectionGroup) => g.id, orderedIds).map(
          (g, i) => ({ ...g, sort_order: i }),
        );
        queryClient.setQueryData<CollectionsListResponse>(collectionKeys.list(), {
          ...snapshot,
          groups,
        });
      }
      return { snapshot };
    },
    onError: (err, _vars, ctx) => {
      if (ctx?.snapshot) queryClient.setQueryData(collectionKeys.list(), ctx.snapshot);
      toast.error(collectionMutationMessage(err, "Failed to reorder groups"));
      if (err instanceof V2ProblemError && err.status === 412)
        void invalidateUserCollectionQueries(queryClient);
    },
    onSettled: () => invalidateUserCollectionQueries(queryClient),
  });
}

export function useReorderCollectionItems(
  collectionId: string,
  source: "user" | "library" = "user",
) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ orderedIds, etag }: { orderedIds: string[]; etag: string }) =>
      v2(
        source === "user"
          ? "PUT /api/v2/collections/{id}/items/order"
          : "PUT /api/v2/admin/collections/{id}/items/order",
        {
          headers: { "If-Match": requiredETag(etag) },
          path: { id: collectionId },
          body: { ordered_ids: orderedIds },
        },
      ),
    onError: (err) => {
      toast.error(collectionMutationMessage(err, "Failed to reorder items"));
      if (source === "user" && err instanceof V2ProblemError && err.status === 412)
        void invalidateUserCollectionQueries(queryClient);
    },
    onSettled: () =>
      source === "user"
        ? invalidateUserCollectionQueries(queryClient, collectionId)
        : invalidateAdminCollectionQueries(queryClient),
  });
}

export function useDeleteUserCollectionImage() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id }: { id: string; type: "poster" }) =>
      v2("DELETE /api/v2/collections/{id}/image", { path: { id }, query: { type: "poster" } }).then(
        () => id,
      ),
    onSuccess: (id) => {
      toast.success("Poster removed");
      return invalidateUserCollectionQueries(queryClient, id);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove poster");
    },
  });
}

export interface SetCollectionSortPreferenceInput {
  collection_kind: "library" | "user" | "watchlist" | "favorites";
  collection_id?: string;
  field: string;
  order: NonNullable<CollectionSortConfig["order"]> | "";
  /** Profile authority captured when the viewer picked this sort. */
  profileAuth: ProfileRequestContextSnapshot;
}

// One serialized write chain per query client. A TanStack mutation would order
// these just as well, but its variables — which carry the access and PIN tokens
// on the profile snapshot — stay in the mutation cache after the write settles,
// and the snapshot contract in api/client.ts forbids that. A private promise
// chain keeps the ordering without ever putting credentials in cached state.
const sortPreferenceWriteQueues = new WeakMap<object, { tail: Promise<unknown> }>();

function sortPreferenceWriteQueue(queryClient: object) {
  let queue = sortPreferenceWriteQueues.get(queryClient);
  if (!queue) {
    queue = { tail: Promise.resolve() };
    sortPreferenceWriteQueues.set(queryClient, queue);
  }
  return queue;
}

/**
 * Persists the sort a viewer picked while browsing a collection, watchlist, or
 * favorites. Sending an empty field pins the viewer to source order — distinct
 * from clearing a collection preference, which restores its configured default.
 *
 * Writes are serialized so an earlier, slower request cannot land after a later
 * choice and overwrite the preference the viewer actually selected last.
 *
 * Failures are deliberately silent: the sort is already applied to the current
 * view through the URL, and a toast for a preference that will be re-sent on
 * the next change would be noise.
 *
 * The write carries the profile authority captured when the viewer picked the
 * sort. These preferences are profile-scoped and the writes are queued, so one
 * that executed under whatever profile happened to be active on send could
 * otherwise land on a household member who never chose it.
 */
export function useSetCollectionSortPreference() {
  const queryClient = useQueryClient();
  return useCallback(
    ({ profileAuth, ...body }: SetCollectionSortPreferenceInput): Promise<void> => {
      const queue = sortPreferenceWriteQueue(queryClient);
      queue.tail = queue.tail
        .catch(() => undefined)
        .then(async () => {
          if (!isProfileRequestContextCurrent(profileAuth)) return;
          try {
            await v2("PUT /api/v2/collections/sort-preference", {
              profileContext: profileAuth,
              body,
            });
          } catch {
            return;
          }
          // The next visit resolves through the server, so drop cached catalog
          // pages built against the previous effective sort. Skip it when the
          // viewer has since switched profiles: those pages belong to someone
          // else, and refetching them would cancel their in-flight first load.
          if (!isCapturedProfileAuthorityActive(profileAuth)) return;
          queryClient.invalidateQueries({ queryKey: catalogKeys.all });
        });
      return queue.tail as Promise<void>;
    },
    [queryClient],
  );
}
