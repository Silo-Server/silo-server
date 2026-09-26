import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { v2 } from "@/api/v2/request";
import { requiredETag } from "@/api/personalCollections";
import {
  fetchAdminGroups,
  fetchAdminCollections,
  adminGroupFromV2,
  adminMutationMessage,
} from "@/api/adminCollections";
import { invalidateAdminCollectionQueries } from "../collectionSurfaceRefresh";
import type { GroupSortMode } from "@/api/types";
import { adminKeys } from "../keys";

const ADMIN_STALE_TIME = 30_000;

// ----- Queries -----

export function useCollectionGroups(libraryId: number | undefined) {
  return useQuery({
    queryKey:
      libraryId !== undefined
        ? adminKeys.collectionGroups(libraryId)
        : ["admin", "collection-groups-board", "none"],
    queryFn: async () => {
      const res = await fetchAdminGroups(libraryId!);
      return res.groups;
    },
    staleTime: ADMIN_STALE_TIME,
    enabled: typeof libraryId === "number" && libraryId > 0,
  });
}

/**
 * Combined board: groups + their collections (resolved client-side from
 * /admin/collections?library_id=N filtered by group_id) + the ungrouped bucket.
 * The backend currently doesn't return collections nested under groups in the
 * admin list; we fetch both and merge here.
 */
export function useAdminCollectionsBoard(libraryId: number | undefined) {
  return useQuery({
    queryKey:
      libraryId !== undefined
        ? [...adminKeys.collectionGroups(libraryId), "with-collections"]
        : ["admin", "collection-groups-board", "none"],
    queryFn: async () => {
      const [groupsResp, collectionsResp] = await Promise.all([
        fetchAdminGroups(libraryId!),
        fetchAdminCollections(libraryId),
      ]);
      const collections = collectionsResp.collections;
      const groups = groupsResp.groups
        .slice()
        .sort((a, b) => a.sort_order - b.sort_order || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
        .map((g) => ({
          ...g,
          collections: collections
            .filter((c) => c.group_id === g.id)
            .sort(
              (a, b) => a.sort_order - b.sort_order || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0),
            ),
        }));
      const ungrouped = collections
        .filter((c) => !c.group_id)
        .sort((a, b) => a.sort_order - b.sort_order || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
      const ungroupedSortOrder: number = groupsResp.ungrouped_sort_order ?? 9999;
      return { groups, ungrouped, ungroupedSortOrder };
    },
    staleTime: ADMIN_STALE_TIME,
    enabled: typeof libraryId === "number" && libraryId > 0,
  });
}

// ----- Mutations -----

interface CreateGroupInput {
  name: string;
  slug?: string;
  default_sort_mode?: GroupSortMode;
}

export function useCreateCollectionGroup(libraryId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (input: CreateGroupInput) =>
      v2("POST /api/v2/admin/libraries/{library_id}/collection-groups", {
        path: { library_id: String(libraryId) },
        body: input,
      }).then(adminGroupFromV2),
    onSuccess: () => {
      void invalidateAdminCollectionQueries(queryClient);
      toast.success("Group created");
    },
    onError: (e) => {
      toast.error(adminMutationMessage(e, "Failed to create group"));
      void invalidateAdminCollectionQueries(queryClient);
    },
  });
}

interface UpdateGroupInput {
  id: string;
  etag: string;
  name?: string;
  slug?: string;
  default_sort_mode?: GroupSortMode;
}

export function useUpdateCollectionGroup(_libraryId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag, ...patch }: UpdateGroupInput) =>
      v2("PATCH /api/v2/admin/collection-groups/{id}", {
        path: { id },
        headers: { "If-Match": requiredETag(etag) },
        body: patch,
      }).then(adminGroupFromV2),
    onSuccess: () => {
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (e) => {
      toast.error(adminMutationMessage(e, "Failed to update group"));
      void invalidateAdminCollectionQueries(queryClient);
    },
  });
}

export function useDeleteCollectionGroup(_libraryId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag }: { id: string; etag: string }) =>
      v2("DELETE /api/v2/admin/collection-groups/{id}", {
        path: { id },
        headers: { "If-Match": requiredETag(etag) },
      }),
    onSuccess: () => {
      void invalidateAdminCollectionQueries(queryClient);
      toast.success("Group deleted");
    },
    onError: (e) => {
      toast.error(adminMutationMessage(e, "Failed to delete group"));
      void invalidateAdminCollectionQueries(queryClient);
    },
  });
}

export function useReorderCollectionGroups(libraryId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ orderedIDs, etag }: { orderedIDs: string[]; etag: string }) =>
      v2("PUT /api/v2/admin/libraries/{library_id}/collection-groups/order", {
        path: { library_id: String(libraryId) },
        headers: { "If-Match": requiredETag(etag) },
        body: { ordered_ids: orderedIDs },
      }),
    onSuccess: () => {
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (e) => {
      toast.error(adminMutationMessage(e, "Failed to reorder groups"));
      void invalidateAdminCollectionQueries(queryClient);
    },
  });
}

interface ReorderCollectionsInput {
  etag: string;
  groupID: string; // pass "ungrouped" for the NULL bucket
  orderedIDs: string[];
  moveOmitted?: "ungrouped";
  libraryId?: number; // required when groupID === "ungrouped"
}

export function useReorderCollectionsInGroup(libraryId: number) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      groupID,
      orderedIDs,
      etag,
      moveOmitted,
      libraryId: libIDArg,
    }: ReorderCollectionsInput) => {
      return v2("PUT /api/v2/admin/collection-groups/{group_id}/collections/order", {
        path: { group_id: groupID },
        query: { move_omitted: moveOmitted, library_id: String(libIDArg ?? libraryId) },
        headers: { "If-Match": requiredETag(etag) },
        body: { ordered_ids: orderedIDs },
      });
    },
    onSuccess: () => {
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (e) => {
      toast.error(adminMutationMessage(e, "Failed to reorder collections"));
      void invalidateAdminCollectionQueries(queryClient);
    },
  });
}
