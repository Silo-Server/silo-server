import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiClientError, api } from "@/api/client";
import type {
  CreateLibraryCollectionRequest,
  ImportMDBListCollectionRequest,
  ImportMDBListCollectionResponse,
  ImportTMDBCollectionRequest,
  ImportTMDBCollectionResponse,
  ImportTraktCollectionRequest,
  ImportTraktCollectionResponse,
  LibraryCollection,
  LibraryCollectionGroup,
  LibraryCollectionSyncRun,
  LibraryCollectionsListResponse,
  UpdateLibraryCollectionRequest,
  AdminJob,
  AdminJobsResponse,
} from "@/api/types";
import type {
  ApplyCollectionTemplateBundleJobRequest,
  ApplyCollectionTemplateBundleRequest,
  ApplyCollectionTemplateBundleResponse,
} from "@/lib/collectionTemplates";
import { adminKeys, sectionKeys } from "../keys";
import { invalidateAdminCollectionQueries } from "../collectionSurfaceRefresh";

const ADMIN_STALE_TIME = 30_000;

function isLikelyRequestTimeout(error: unknown): boolean {
  if (error instanceof ApiClientError) {
    return (
      error.status === 408 || error.status === 502 || error.status === 503 || error.status === 504
    );
  }
  return error instanceof TypeError;
}

function applyTemplateBundleErrorMessage(error: unknown): string {
  if (isLikelyRequestTimeout(error)) {
    return "The apply request timed out. Silo may still be creating collections; refresh in a minute.";
  }
  return error instanceof Error ? error.message : "Failed to apply defaults";
}

function buildCollectionFormData(
  data: Record<string, unknown>,
  poster?: File | null,
  backdrop?: File | null,
): FormData | string {
  if (!poster && !backdrop) {
    return JSON.stringify(data);
  }
  const formData = new FormData();
  formData.append("data", JSON.stringify(data));
  if (poster) formData.append("poster", poster);
  if (backdrop) formData.append("backdrop", backdrop);
  return formData;
}

function fetchAdminCollections(libraryId?: number): Promise<LibraryCollectionsListResponse> {
  const query = libraryId ? `?library_id=${libraryId}` : "";
  return api<LibraryCollectionsListResponse>(`/admin/collections${query}`).then((data) => ({
    collections: data.collections ?? [],
    groups: data.groups ?? [],
  }));
}

export function useAdminCollections(libraryId?: number) {
  return useQuery({
    queryKey: adminKeys.collections(libraryId),
    queryFn: () => fetchAdminCollections(libraryId),
    select: (data) => data.collections,
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminCollectionGroups(libraryId?: number) {
  return useQuery({
    queryKey: adminKeys.collectionGroups(libraryId),
    queryFn: () =>
      api<{ groups: LibraryCollectionGroup[]; ungrouped_sort_order: number }>(
        `/admin/libraries/${libraryId}/collection-groups`,
      ).then((data) => data.groups ?? []),
    staleTime: ADMIN_STALE_TIME,
    enabled: libraryId !== undefined,
  });
}

export function useCreateAdminCollection() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      body,
      poster,
      backdrop,
    }: {
      body: CreateLibraryCollectionRequest;
      poster?: File | null;
      backdrop?: File | null;
    }) => {
      const payload = buildCollectionFormData(
        body as unknown as Record<string, unknown>,
        poster,
        backdrop,
      );
      return api<LibraryCollection>("/admin/collections", {
        method: "POST",
        body: payload,
      });
    },
    onSuccess: (_collection) => {
      toast.success("Collection created");
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to save");
    },
  });
}

export function useApplyCollectionTemplateBundle() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      bundleId,
      body,
    }: {
      bundleId: string;
      body: ApplyCollectionTemplateBundleRequest;
    }) =>
      api<ApplyCollectionTemplateBundleResponse>(
        `/admin/collections/template-bundles/${encodeURIComponent(bundleId)}/apply`,
        {
          method: "POST",
          body: JSON.stringify(body),
        },
      ),
    onSuccess: (result) => {
      if (!result.dry_run) {
        const created = result.created.length;
        const deleted = result.deleted?.length ?? 0;
        const syncQueued = result.sync_queued?.length ?? 0;
        const failed = result.failed.length;
        const deleteFailed = result.delete_failed?.length ?? 0;
        const failureCount = failed + deleteFailed;
        if (created > 0 || deleted > 0 || syncQueued > 0) {
          const message = [
            deleted > 0 ? `Deleted ${deleted}` : "",
            created > 0 ? `created ${created}` : "",
            syncQueued > 0 ? `queued ${syncQueued} syncs` : "",
            failureCount > 0 ? `${failureCount} failed` : "",
          ]
            .filter(Boolean)
            .join("; ");
          toast.success(message);
        }
        void invalidateAdminCollectionQueries(queryClient);
        void queryClient.invalidateQueries({ queryKey: sectionKeys.all });
      }
    },
    onError: (error) => {
      toast.error(applyTemplateBundleErrorMessage(error));
    },
  });
}

export function useQueueCollectionTemplateBundleApply() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      bundleId,
      body,
    }: {
      bundleId: string;
      body: ApplyCollectionTemplateBundleJobRequest;
    }) =>
      api<AdminJob>(
        `/admin/collections/template-bundles/${encodeURIComponent(bundleId)}/apply-job`,
        {
          method: "POST",
          body: JSON.stringify(body),
        },
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: adminKeys.jobs("template_bundle_apply") });
      void queryClient.invalidateQueries({ queryKey: adminKeys.jobs("__all") });
      toast.success("Applying collection defaults in the background");
    },
    onError: (error) => {
      if (error instanceof ApiClientError && error.status === 409) {
        toast.error("A collection defaults apply is already running");
        return;
      }
      toast.error(error instanceof Error ? error.message : "Failed to queue collection defaults");
    },
  });
}

export function useTemplateBundleApplyJobs() {
  return useQuery({
    queryKey: adminKeys.jobs("template_bundle_apply"),
    queryFn: () =>
      api<AdminJobsResponse>("/admin/jobs?job_type=template_bundle_apply&limit=10").then(
        (data) => data.jobs ?? [],
      ),
    staleTime: 0,
    refetchInterval: 5000,
    refetchIntervalInBackground: true,
  });
}

export function useUpdateAdminCollection() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      id,
      body,
      poster,
      backdrop,
    }: {
      id: string;
      body: UpdateLibraryCollectionRequest;
      poster?: File | null;
      backdrop?: File | null;
    }) => {
      const payload = buildCollectionFormData(
        body as unknown as Record<string, unknown>,
        poster,
        backdrop,
      );
      return api<LibraryCollection>(`/admin/collections/${id}`, {
        method: "PUT",
        body: payload,
      });
    },
    onSuccess: (_collection) => {
      toast.success("Collection updated");
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to save");
    },
  });
}

export function useDeleteAdminCollection() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, libraryId }: { id: string; libraryId: number }) =>
      api<void>(`/admin/collections/${id}`, {
        method: "DELETE",
      }).then(() => libraryId),
    onSuccess: (_libraryId) => {
      toast.success("Collection deleted");
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to delete");
    },
  });
}

export interface ReorderAdminCollectionsArgs {
  libraryId: number;
  orderedIds: string[];
  groupId?: string | null;
}

export function useReorderAdminCollections() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ libraryId, orderedIds, groupId }: ReorderAdminCollectionsArgs) =>
      api<void>("/admin/collections/order", {
        method: "PUT",
        body: JSON.stringify({
          library_id: libraryId,
          ordered_ids: orderedIds,
          ...(groupId !== undefined ? { group_id: groupId } : {}),
        }),
      }),
    onMutate: async ({ libraryId, orderedIds, groupId }) => {
      const key = adminKeys.collections(libraryId);
      await queryClient.cancelQueries({ queryKey: key });
      const snapshot = queryClient.getQueryData<LibraryCollectionsListResponse>(key);
      if (snapshot) {
        const inScope = (c: LibraryCollection) =>
          groupId === undefined ? true : (c.group_id ?? null) === groupId;
        // Clone before stamping sort_order so the rollback snapshot retains
        // the pre-mutation values; in-place mutation on shared object refs
        // would corrupt onError's restore.
        const reordered = (() => {
          const byId = new Map(snapshot.collections.filter(inScope).map((c) => [c.id, c]));
          const out: LibraryCollection[] = [];
          for (const id of orderedIds) {
            const c = byId.get(id);
            if (c) out.push({ ...c, sort_order: out.length });
          }
          return out;
        })();
        const next = [...snapshot.collections];
        let cursor = 0;
        for (let i = 0; i < next.length; i++) {
          const current = next[i];
          if (current && inScope(current)) {
            next[i] = reordered[cursor++] ?? current;
          }
        }
        queryClient.setQueryData<LibraryCollectionsListResponse>(key, {
          ...snapshot,
          collections: next,
        });
      }
      return { snapshot, libraryId };
    },
    onError: (error, _vars, ctx) => {
      if (ctx?.snapshot) {
        queryClient.setQueryData(adminKeys.collections(ctx.libraryId), ctx.snapshot);
      }
      toast.error(error instanceof Error ? error.message : "Failed to reorder");
    },
    onSettled: () => invalidateAdminCollectionQueries(queryClient),
  });
}

export function useCreateAdminCollectionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      libraryId,
      name,
      slug,
      defaultSortMode,
    }: {
      libraryId: number;
      name: string;
      slug?: string;
      defaultSortMode?: string;
    }) =>
      api<LibraryCollectionGroup>(`/admin/libraries/${libraryId}/collection-groups`, {
        method: "POST",
        body: JSON.stringify({ name, slug, default_sort_mode: defaultSortMode }),
      }),
    onSuccess: () => invalidateAdminCollectionQueries(queryClient),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to add group"),
  });
}

export function useUpdateAdminCollectionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      name,
      defaultSortMode,
    }: {
      id: string;
      name?: string;
      defaultSortMode?: string;
    }) =>
      api<LibraryCollectionGroup>(`/admin/collection-groups/${encodeURIComponent(id)}`, {
        method: "PUT",
        body: JSON.stringify({ name, default_sort_mode: defaultSortMode }),
      }),
    onSuccess: () => invalidateAdminCollectionQueries(queryClient),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to rename group"),
  });
}

export function useDeleteAdminCollectionGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id }: { libraryId: number; id: string }) =>
      api<void>(`/admin/collection-groups/${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    onSuccess: () => invalidateAdminCollectionQueries(queryClient),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to delete group"),
  });
}

export function useReorderAdminCollectionGroups() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ libraryId, orderedIds }: { libraryId: number; orderedIds: string[] }) =>
      api<void>(`/admin/libraries/${libraryId}/collection-groups/reorder`, {
        method: "PUT",
        body: JSON.stringify({ ids: orderedIds }),
      }),
    onMutate: async ({ libraryId, orderedIds }) => {
      const key = adminKeys.collectionGroups(libraryId);
      await queryClient.cancelQueries({ queryKey: key });
      const snapshot = queryClient.getQueryData<LibraryCollectionGroup[]>(key);
      if (snapshot) {
        const byId = new Map(snapshot.map((g) => [g.id, g]));
        const reordered: LibraryCollectionGroup[] = [];
        for (const id of orderedIds) {
          const g = byId.get(id);
          if (g) reordered.push({ ...g, sort_order: reordered.length });
        }
        queryClient.setQueryData<LibraryCollectionGroup[]>(key, reordered);
      }
      return { snapshot, libraryId };
    },
    onError: (err, _vars, ctx) => {
      if (ctx?.snapshot) {
        queryClient.setQueryData(adminKeys.collectionGroups(ctx.libraryId), ctx.snapshot);
      }
      toast.error(err instanceof Error ? err.message : "Failed to reorder groups");
    },
    onSettled: () => invalidateAdminCollectionQueries(queryClient),
  });
}

export function useReorderAdminCollectionItems(collectionId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (orderedIds: string[]) =>
      api<void>(`/admin/collections/${collectionId}/items/order`, {
        method: "PUT",
        body: JSON.stringify({ ordered_ids: orderedIds }),
      }),
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to reorder items");
    },
    onSettled: () => invalidateAdminCollectionQueries(queryClient),
  });
}

export function useDeleteCollectionImage() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      id,
      type,
      libraryId,
    }: {
      id: string;
      type: "poster" | "backdrop";
      libraryId: number;
    }) =>
      api<void>(`/admin/collections/${id}/image?type=${type}`, {
        method: "DELETE",
      }).then(() => libraryId),
    onSuccess: (_libraryId) => {
      toast.success("Image removed");
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to remove image");
    },
  });
}

export function useSyncAdminCollection() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ id, libraryId }: { id: string; libraryId: number }) =>
      api<LibraryCollectionSyncRun>(`/admin/collections/${id}/sync`, {
        method: "POST",
      }).then((data) => ({ data, libraryId })),
    onSuccess: ({ data, libraryId: _libraryId }) => {
      toast.success(
        data.status === "warning" ? "Collection synced with warnings" : "Collection synced",
      );
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Sync failed");
    },
  });
}

export function useImportMDBListCollection() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      body,
      poster,
      backdrop,
    }: {
      body: ImportMDBListCollectionRequest;
      poster?: File | null;
      backdrop?: File | null;
    }) => {
      const payload = buildCollectionFormData(
        body as unknown as Record<string, unknown>,
        poster,
        backdrop,
      );
      return api<ImportMDBListCollectionResponse>("/admin/collections/import/mdblist", {
        method: "POST",
        body: payload,
      });
    },
    onSuccess: (result) => {
      toast.success(
        result.sync_run?.status === "warning"
          ? "MDBList imported with warnings"
          : "MDBList imported",
      );
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Import failed");
    },
  });
}

export function useImportTMDBCollection() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      body,
      poster,
      backdrop,
    }: {
      body: ImportTMDBCollectionRequest;
      poster?: File | null;
      backdrop?: File | null;
    }) => {
      const payload = buildCollectionFormData(
        body as unknown as Record<string, unknown>,
        poster,
        backdrop,
      );
      return api<ImportTMDBCollectionResponse>("/admin/collections/import/tmdb", {
        method: "POST",
        body: payload,
      });
    },
    onSuccess: (result) => {
      toast.success(
        result.sync_run?.status === "warning"
          ? "TMDB collection imported with warnings"
          : "TMDB collection imported",
      );
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Import failed");
    },
  });
}

export function useImportTraktCollection() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({
      body,
      poster,
      backdrop,
    }: {
      body: ImportTraktCollectionRequest;
      poster?: File | null;
      backdrop?: File | null;
    }) => {
      const payload = buildCollectionFormData(
        body as unknown as Record<string, unknown>,
        poster,
        backdrop,
      );
      return api<ImportTraktCollectionResponse>("/admin/collections/import/trakt", {
        method: "POST",
        body: payload,
      });
    },
    onSuccess: (result) => {
      const statusMessages: Record<string, string> = {
        warning: "Trakt collection imported with warnings",
        failed: "Trakt collection imported but sync failed",
      };
      const status = result.sync_run?.status ?? "";
      toast.success(statusMessages[status] ?? "Trakt collection imported");
      void invalidateAdminCollectionQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Import failed");
    },
  });
}
