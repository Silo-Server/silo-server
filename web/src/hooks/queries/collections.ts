import { fetchAdminItemOrderSnapshot } from "@/api/adminCollections";
import {
  isCapturedProfileAuthorityActive,
  isProfileRequestContextCurrent,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import {
  collectionsFromV2,
  fetchCollectionEditSnapshot,
  fetchItemOrderSnapshot,
  serverCollectionsFromV2,
} from "@/api/personalCollections";
import type {
  CollectionCapabilitiesResponse,
  CollectionsListResponse,
  CollectionSortConfig,
} from "@/api/types";
import { requiredETag } from "@/api/v2/etag";
import { v2, V2ProblemError } from "@/api/v2/request";
import { PERSONAL_SCOPE } from "@/lib/collections/scope";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback } from "react";
import { toast } from "sonner";
import { putCollectionItem, useScopeDelete } from "./collectionScope";
import { invalidateAdminCollectionQueries } from "./collectionSurfaceRefresh";
import { catalogKeys, collectionKeys } from "./keys";

const collectionMutationMessage = PERSONAL_SCOPE.errorMessage;

export function useCollections() {
  return useQuery({
    queryKey: PERSONAL_SCOPE.keys.list,
    queryFn: PERSONAL_SCOPE.fetchList,
    select: (data) => data.collections,
  });
}

export function useCollectionCapabilities(enabled = true) {
  return useQuery({
    queryKey: PERSONAL_SCOPE.keys.capabilities,
    queryFn: () =>
      v2("GET /api/v2/collections/capabilities").then((value) => ({
        ...value,
        display_filter_presets:
          value.display_filter_presets as CollectionCapabilitiesResponse["display_filter_presets"],
      })),
    enabled,
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

/** Titles per page of a collection's item list; the editor lists one page. */
export const COLLECTION_ITEMS_PAGE = 200;

export function useCollectionItems(
  collectionId: string,
  cursor = "",
  source: "user" | "library" = "user",
  enabled = true,
) {
  return useQuery({
    enabled,
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
          query: { limit: COLLECTION_ITEMS_PAGE, ...(cursor ? { cursor } : {}) },
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

/**
 * Turns sharing on or off from a list (#1615). The collection is read first,
 * so the write carries its current ETag even when the list is old.
 */
export function useSetCollectionShared() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async ({ id, shared }: { id: string; shared: boolean }) => {
      const { etag } = await fetchCollectionEditSnapshot(id);
      return v2("PATCH /api/v2/collections/{id}", {
        path: { id },
        headers: { "If-Match": requiredETag(etag) },
        body: { is_shared: shared },
      });
    },
    onSuccess: (_collection, { id, shared }) => {
      toast.success(shared ? "Shown to other profiles" : "Only you see it now");
      return PERSONAL_SCOPE.invalidate(queryClient, id);
    },
    onError: (err) => {
      toast.error(collectionMutationMessage(err, "Couldn't change sharing"));
      if (err instanceof V2ProblemError && err.status === 412)
        void PERSONAL_SCOPE.invalidate(queryClient);
    },
  });
}

export function useDeleteCollection() {
  return useScopeDelete(PERSONAL_SCOPE);
}

/** The profile's collections, each own manual one marked with whether it holds `contentId`. */
export function useCollectionsContaining(contentId: string) {
  return useQuery({
    queryKey: collectionKeys.containing(contentId),
    queryFn: () =>
      v2("GET /api/v2/collections", { query: { contains_item: contentId } }).then(
        collectionsFromV2,
      ),
    select: (data) => data.collections,
  });
}

/** Adds one title to one of the profile's own manual collections. */
export function useAddItemToCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ collectionId, mediaItemId }: { collectionId: string; mediaItemId: string }) =>
      putCollectionItem(PERSONAL_SCOPE, collectionId, mediaItemId, 0),
    onSuccess: (_data, vars) => PERSONAL_SCOPE.invalidate(queryClient, vars.collectionId),
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
        ? PERSONAL_SCOPE.invalidate(queryClient, collectionId)
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
  /** Each of the acting profile's own collections, in the new order. */
  orderedIds: string[];
  etag: string;
}

export function useReorderCollections() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ orderedIds, etag }: ReorderCollectionsArgs) =>
      v2("PUT /api/v2/collections/order", {
        headers: { "If-Match": requiredETag(etag) },
        body: { ordered_ids: orderedIds },
      }),
    onMutate: async ({ orderedIds }) => {
      await queryClient.cancelQueries({ queryKey: collectionKeys.list() });
      const snapshot = queryClient.getQueryData<CollectionsListResponse>(collectionKeys.list());
      if (snapshot) {
        // Only the reordered (own) collections move, within the slots they
        // already hold; other profiles' shared collections stay in place.
        // Clone before stamping sort_order so the snapshot kept for rollback
        // retains its original values.
        const moved = new Set(orderedIds);
        const reordered = reorderByIds(
          snapshot.collections.filter((c) => moved.has(c.id)),
          (c) => c.id,
          orderedIds,
        ).map((c, i) => ({ ...c, sort_order: i }));
        let cursor = 0;
        const collections = snapshot.collections.map((c) =>
          moved.has(c.id) ? (reordered[cursor++] ?? c) : c,
        );
        queryClient.setQueryData<CollectionsListResponse>(collectionKeys.list(), {
          ...snapshot,
          collections,
        });
      }
      return { snapshot };
    },
    onError: (err, _vars, ctx) => {
      if (ctx?.snapshot) queryClient.setQueryData(collectionKeys.list(), ctx.snapshot);
      toast.error(collectionMutationMessage(err, "Failed to reorder"));
      if (err instanceof V2ProblemError && err.status === 412)
        void PERSONAL_SCOPE.invalidate(queryClient);
    },
    onSettled: () => PERSONAL_SCOPE.invalidate(queryClient),
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
        void PERSONAL_SCOPE.invalidate(queryClient);
    },
    onSettled: () =>
      source === "user"
        ? PERSONAL_SCOPE.invalidate(queryClient, collectionId)
        : invalidateAdminCollectionQueries(queryClient),
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
