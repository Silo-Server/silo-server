import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type {
  Collection,
  CollectionCapabilitiesResponse,
  CollectionGroup,
  CollectionItem,
  CollectionsListResponse,
  CreateCollectionRequest,
  ServerCollectionsResponse,
  UpdateCollectionRequest,
} from "@/api/types";
import { collectionKeys } from "./keys";
import { toast } from "sonner";
import { invalidateUserCollectionQueries } from "./collectionSurfaceRefresh";

// Single fetcher for /collections — both useCollections and useCollectionGroups
// share the cache so the page makes one network round-trip.
function fetchCollectionsList(): Promise<CollectionsListResponse> {
  return api<CollectionsListResponse>("/collections").then((d) => ({
    collections: d.collections ?? [],
    groups: d.groups ?? [],
  }));
}

function buildUserCollectionPayload(
  data: Record<string, unknown>,
  poster?: File | null,
): FormData | string {
  if (!poster) {
    return JSON.stringify(data);
  }
  const formData = new FormData();
  formData.append("data", JSON.stringify(data));
  formData.append("poster", poster);
  return formData;
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
    queryFn: () => api<CollectionCapabilitiesResponse>("/collections/capabilities"),
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
    queryFn: () =>
      api<ServerCollectionsResponse>("/collections/server").then((d) => d.libraries ?? []),
  });
}

export function useCollectionItems(collectionId: string) {
  return useQuery({
    queryKey: collectionKeys.items(collectionId),
    queryFn: () =>
      api<{ items: CollectionItem[] }>(`/collections/${collectionId}/items`).then(
        (d) => d.items ?? [],
      ),
  });
}

export function useCreateCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ body, poster }: { body: CreateCollectionRequest; poster?: File | null }) =>
      api("/collections", {
        method: "POST",
        body: buildUserCollectionPayload(body as unknown as Record<string, unknown>, poster),
      }),
    onSuccess: () => {
      toast.success("Collection created");
      return invalidateUserCollectionQueries(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save");
    },
  });
}

export function useUpdateCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      body,
      poster,
    }: {
      id: string;
      body: UpdateCollectionRequest;
      poster?: File | null;
    }) =>
      api(`/collections/${id}`, {
        method: "PUT",
        body: buildUserCollectionPayload(body as unknown as Record<string, unknown>, poster),
      }),
    onSuccess: (_data, { id }) => {
      toast.success("Collection updated");
      return invalidateUserCollectionQueries(queryClient, id);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save");
    },
  });
}

export function useDeleteCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api(`/collections/${id}`, { method: "DELETE" }),
    onSuccess: (_data, id) => {
      toast.success("Collection deleted");
      return invalidateUserCollectionQueries(queryClient, id);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete");
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
      const path =
        source === "user"
          ? `/collections/${collectionId}/items/${mediaItemId}`
          : `/admin/collections/${collectionId}/items/${mediaItemId}`;
      return api<void>(path, {
        method: "PUT",
        body: JSON.stringify({ position: position ?? 0 }),
      });
    },
    onSuccess: (_data, vars) => {
      toast.success("Added to collection");
      if (vars.source === "user") {
        return invalidateUserCollectionQueries(queryClient, vars.collectionId);
      }
      // Library collection: invalidate its items query.
      queryClient.invalidateQueries({
        queryKey: ["libraryCollections", "items", vars.collectionId],
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to add to collection");
    },
  });
}

export function useRemoveCollectionItem(collectionId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (mediaItemId: string) =>
      api<void>(`/collections/${collectionId}/items/${mediaItemId}`, { method: "DELETE" }),
    onSuccess: () => invalidateUserCollectionQueries(queryClient, collectionId),
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
  groupId?: string | null;
}

export function useReorderCollections() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ orderedIds, groupId }: ReorderCollectionsArgs) =>
      api<void>("/collections/order", {
        method: "PUT",
        body: JSON.stringify({
          ordered_ids: orderedIds,
          ...(groupId !== undefined ? { group_id: groupId } : {}),
        }),
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
      toast.error(err instanceof Error ? err.message : "Failed to reorder");
    },
    onSettled: () => invalidateUserCollectionQueries(queryClient),
  });
}

export function useCreateCollectionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ name, slug }: { name: string; slug?: string }) =>
      api<CollectionGroup>("/collections/groups", {
        method: "POST",
        body: JSON.stringify({ name, slug }),
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
    mutationFn: ({ id, name }: { id: string; name: string }) =>
      api<CollectionGroup>(`/collections/groups/${encodeURIComponent(id)}`, {
        method: "PUT",
        body: JSON.stringify({ name }),
      }),
    onSuccess: () => invalidateUserCollectionQueries(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to rename group");
    },
  });
}

export function useDeleteCollectionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api<void>(`/collections/groups/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => invalidateUserCollectionQueries(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete group");
    },
  });
}

export function useReorderCollectionGroups() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (orderedIds: string[]) =>
      api<void>("/collections/groups/order", {
        method: "PUT",
        body: JSON.stringify({ ordered_ids: orderedIds }),
      }),
    onMutate: async (orderedIds) => {
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
      toast.error(err instanceof Error ? err.message : "Failed to reorder groups");
    },
    onSettled: () => invalidateUserCollectionQueries(queryClient),
  });
}

export function useReorderCollectionItems(collectionId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (orderedIds: string[]) =>
      api<void>(`/collections/${collectionId}/items/order`, {
        method: "PUT",
        body: JSON.stringify({ ordered_ids: orderedIds }),
      }),
    onMutate: async (orderedIds) => {
      const key = collectionKeys.items(collectionId);
      await queryClient.cancelQueries({ queryKey: key });
      const snapshot = queryClient.getQueryData<CollectionItem[]>(key);
      if (snapshot) {
        queryClient.setQueryData(
          key,
          reorderByIds(snapshot, (item) => item.media_item_id, orderedIds),
        );
      }
      return { snapshot };
    },
    onError: (err, _vars, ctx) => {
      if (ctx?.snapshot) {
        queryClient.setQueryData(collectionKeys.items(collectionId), ctx.snapshot);
      }
      toast.error(err instanceof Error ? err.message : "Failed to reorder items");
    },
    onSettled: () => invalidateUserCollectionQueries(queryClient, collectionId),
  });
}

export function useDeleteUserCollectionImage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id }: { id: string; type: "poster" }) =>
      api<void>(`/collections/${id}/image?type=poster`, { method: "DELETE" }).then(() => id),
    onSuccess: (id) => {
      toast.success("Poster removed");
      return invalidateUserCollectionQueries(queryClient, id);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove poster");
    },
  });
}
