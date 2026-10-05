import { useAdminTaskJobs } from "@/hooks/queries/admin/taskJobs";
import { v2, V2ProblemError } from "@/api/v2/request";
import { adminJobFromV2 } from "@/api/v2/libraries";
import { requiredETag } from "@/api/v2/etag";
import {
  fetchAdminCollections,
  fetchAdminCollectionSnapshot,
  fetchAdminGroups,
  adminCreateBody,
  adminUpdateBody,
  saveAdminArtwork,
  adminMutationMessage,
  templateApplyBody,
  templateResultFromV2,
} from "@/api/adminCollections";
import { useState } from "react";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { CreateLibraryCollectionRequest, UpdateLibraryCollectionRequest } from "@/api/types";
import type {
  ApplyCollectionTemplateBundleJobRequest,
  ApplyCollectionTemplateBundleRequest,
} from "@/lib/collectionTemplates";
import { SERVER_SCOPE } from "@/lib/collections/scope";
import { adminKeys } from "../keys";
import { invalidateAdminCollectionQueries } from "../collectionSurfaceRefresh";
import { runBulkDelete, type BulkDeleteProgress } from "../bulkDelete";

const ADMIN_STALE_TIME = 30_000;

export function useAdminCollectionCapabilities(enabled = true) {
  return useQuery({
    queryKey: SERVER_SCOPE.keys.capabilities,
    queryFn: () => v2("GET /api/v2/admin/collections/capabilities"),
    enabled,
    staleTime: Infinity,
  });
}
function showArtworkErrors(result: { artworkErrors: string[] }) {
  if (result.artworkErrors.length)
    toast.warning("Collection saved, but artwork could not be saved", {
      description: result.artworkErrors.join(". "),
    });
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
    queryFn: () => fetchAdminGroups(libraryId!).then((data) => data.groups),
    staleTime: ADMIN_STALE_TIME,
    enabled: libraryId !== undefined,
  });
}

export function useCreateAdminCollection() {
  const queryClient = useQueryClient();

  return useMutation({
    retry: false,
    mutationFn: ({
      body,
      poster,
      backdrop,
    }: {
      body: CreateLibraryCollectionRequest;
      poster?: File | null;
      backdrop?: File | null;
    }) => {
      return v2("POST /api/v2/admin/collections", { body: adminCreateBody(body) }).then((value) =>
        saveAdminArtwork(value, body, poster, backdrop),
      );
    },
    onSuccess: (result) => {
      showArtworkErrors(result);
      toast.success("Collection created");
      void SERVER_SCOPE.invalidate(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to save");
    },
  });
}

/**
 * A starter pack's dry run: what applying the pack to these libraries would
 * create, keep and leave out. It never deletes existing collections, and
 * carries `featured` only when the admin asked for hero banners.
 */
export function starterPackDryRunQuery(
  packId: string,
  body: Pick<ApplyCollectionTemplateBundleRequest, "library_ids" | "featured">,
) {
  return queryOptions({
    queryKey: adminKeys.starterPackDryRun(packId, body),
    queryFn: () =>
      v2("POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply", {
        path: { bundle_id: packId },
        body: templateApplyBody({ ...body, dry_run: true, delete_existing: false }),
      }).then(templateResultFromV2),
    staleTime: 0,
    retry: false,
  });
}

/** A collection job, polled every 2 seconds until it ends. */
export function collectionJobQuery(jobId: string | null) {
  return queryOptions({
    queryKey: ["admin", "collection-job", jobId],
    queryFn: () => v2("GET /api/v2/admin/collection-jobs/{job_id}", { path: { job_id: jobId! } }),
    enabled: !!jobId,
    refetchInterval: (query) => (query.state.data?.terminal ? false : 2000),
  });
}

export function useQueueCollectionTemplateBundleApply() {
  const queryClient = useQueryClient();

  return useMutation({
    retry: false,
    mutationFn: ({
      bundleId,
      body,
    }: {
      bundleId: string;
      body: ApplyCollectionTemplateBundleJobRequest;
    }) =>
      v2("POST /api/v2/admin/collections/template-bundles/{bundle_id}/apply-job", {
        path: { bundle_id: bundleId },
        body: templateApplyBody(body),
      }),
    onSuccess: (job) => {
      queryClient.setQueryData(["admin", "collection-job", "accepted"], job.id);
      void queryClient.invalidateQueries({ queryKey: adminKeys.jobs("template_bundle_apply") });
      void queryClient.invalidateQueries({ queryKey: adminKeys.jobs("__all") });
    },
    onError: (error) => {
      if (error instanceof V2ProblemError && error.status === 409) {
        toast.error("A starter pack is already being added. Try again when it finishes.");
        return;
      }
      toast.error(error instanceof Error ? error.message : "Couldn't add the starter pack");
    },
  });
}

export function useTemplateBundleApplyJobs() {
  const accepted = useQuery<string | null>({
    queryKey: ["admin", "collection-job", "accepted"],
    queryFn: () => null,
    initialData: null,
    staleTime: Infinity,
  });
  const listed = useAdminTaskJobs("template_bundle_apply", 10);
  const job = useQuery(collectionJobQuery(accepted.data));
  const current = job.data
    ? {
        ...adminJobFromV2(job.data),
        result_payload: job.data.template_result
          ? templateResultFromV2(job.data.template_result)
          : {},
      }
    : null;
  return {
    ...listed,
    data: current
      ? [current, ...(listed.data ?? []).filter((row) => row.id !== current.id)].sort(
          (a, b) => Date.parse(b.requested_at) - Date.parse(a.requested_at),
        )
      : listed.data,
  };
}

export function useUpdateAdminCollection() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      id,
      etag,
      body,
      poster,
      backdrop,
      removeArtwork,
    }: {
      id: string;
      etag: string;
      body: UpdateLibraryCollectionRequest;
      poster?: File | null;
      backdrop?: File | null;
      removeArtwork?: ("poster" | "backdrop")[];
    }) =>
      v2("PATCH /api/v2/admin/collections/{id}", {
        path: { id },
        headers: { "If-Match": requiredETag(etag) },
        body: adminUpdateBody(body),
      }).then((value) => saveAdminArtwork(value, body, poster, backdrop, removeArtwork)),
    onSuccess: (result) => {
      showArtworkErrors(result);
      toast.success("Collection saved");
      void SERVER_SCOPE.invalidate(queryClient);
    },
    onError: (error) => {
      toast.error(SERVER_SCOPE.errorMessage(error, "Failed to save"));
      void SERVER_SCOPE.invalidate(queryClient);
    },
  });
}

/**
 * A list's one-field change. It reads the collection fresh for its ETag and
 * type, then sends only `collection_type` and `field`, so nothing else on the
 * collection can be overwritten by a stale list.
 */
export async function patchAdminCollectionField(
  id: string,
  field: { visibility: "visible" | "hidden" } | { featured: boolean },
) {
  const { collection, etag } = await fetchAdminCollectionSnapshot(id);
  await v2("PATCH /api/v2/admin/collections/{id}", {
    path: { id },
    headers: { "If-Match": requiredETag(etag) },
    body: { collection_type: collection.collection_type, ...field },
  });
}

/** The list's Collections tab switch. */
export function useSetAdminCollectionVisibility() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, visible }: { id: string; visible: boolean }) =>
      patchAdminCollectionField(id, { visibility: visible ? "visible" : "hidden" }),
    onError: (error) => {
      toast.error(SERVER_SCOPE.errorMessage(error, "Couldn't change it"));
    },
    onSettled: () => SERVER_SCOPE.invalidate(queryClient),
  });
}

/** Arrange's Pin to the start of its shelf (`featured`). Settles once the lists are read again. */
export function useSetAdminCollectionPin() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, pinned }: { id: string; pinned: boolean }) =>
      patchAdminCollectionField(id, { featured: pinned }),
    onError: (error, { pinned }) => {
      toast.error(
        SERVER_SCOPE.errorMessage(error, pinned ? "Couldn't pin it" : "Couldn't unpin it"),
      );
    },
    onSettled: () => SERVER_SCOPE.invalidate(queryClient),
  });
}

export function useDeleteAdminCollections() {
  const queryClient = useQueryClient();
  const [progress, setProgress] = useState<BulkDeleteProgress | null>(null);

  const mutation = useMutation({
    onMutate: (ids) => {
      setProgress({ completed: 0, total: new Set(ids.map((entry) => entry.id)).size });
    },
    mutationFn: (snapshots: { id: string; etag: string }[]) =>
      runBulkDelete(
        snapshots.map((entry) => entry.id),
        (id) =>
          v2("DELETE /api/v2/admin/collections/{id}", {
            path: { id },
            headers: { "If-Match": requiredETag(snapshots.find((entry) => entry.id === id)?.etag) },
          }).catch((error) => {
            if (error instanceof V2ProblemError && error.status === 412)
              throw new Error(adminMutationMessage(error, "Failed to delete"));
            throw error;
          }),
        (error) => {
          if (error instanceof V2ProblemError && error.status === 404) {
            return "deleted";
          }
          if (
            error instanceof V2ProblemError &&
            error.status === 409 &&
            error.problemType === "collection_in_use"
          ) {
            return "kept";
          }
          return "failed";
        },
        setProgress,
      ),
    onSuccess: async ({ requested, deleted, kept, failed, firstError }) => {
      const keptDescription = `Kept ${kept} collection${kept === 1 ? "" : "s"} in use by home or library sections`;

      if (failed === 0 && kept === 0) {
        toast.success(`Deleted ${deleted} collection${deleted === 1 ? "" : "s"}`);
      } else if (failed === 0) {
        toast.warning(`Deleted ${deleted} collection${deleted === 1 ? "" : "s"}`, {
          description: keptDescription,
        });
      } else if (deleted > 0 || kept > 0) {
        toast.warning(`Deleted ${deleted} of ${requested} collections`, {
          description: [kept > 0 ? keptDescription : "", firstError].filter(Boolean).join(". "),
        });
      } else {
        toast.error(`Failed to delete ${failed} collection${failed === 1 ? "" : "s"}`, {
          description: firstError,
        });
      }
      await invalidateAdminCollectionQueries(queryClient);
    },
    onSettled: () => {
      setProgress(null);
    },
  });

  return { ...mutation, progress };
}
