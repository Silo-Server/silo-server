import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { AdminJob, ArtworkPurgeRequest, ArtworkStorageAccounting } from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";
import { toast } from "sonner";

const storageKey = adminKeys.artworkStorage();

export function useArtworkStorage() {
  return useQuery({
    queryKey: storageKey,
    queryFn: () => api<ArtworkStorageAccounting>("/admin/artwork/storage"),
    staleTime: 30_000,
  });
}

function useArtworkJob(path: string, success: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body?: unknown) =>
      api<AdminJob>(path, { method: "POST", body: JSON.stringify(body ?? {}) }),
    onSuccess: () => {
      toast.success(success);
      queryClient.invalidateQueries({ queryKey: ["admin", "jobs"] });
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : "Artwork job failed"),
  });
}

export function useRefreshArtworkStorage() {
  return useArtworkJob("/admin/artwork/storage/refresh", "Artwork storage refresh queued");
}

export function useImportArtworkStorage() {
  return useArtworkJob("/admin/artwork/storage/import", "Portable artwork import queued");
}

export function useRebuildArtworkStorage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api<ArtworkStorageAccounting>("/admin/artwork/rebuild", {
        method: "POST",
        body: JSON.stringify({}),
      }),
    onSuccess: () => {
      toast.success("Artwork store rebuild started");
      queryClient.invalidateQueries({ queryKey: storageKey });
    },
    onError: (error) =>
      toast.error(error instanceof Error ? error.message : "Artwork store rebuild failed"),
  });
}

export function usePurgeArtworkStorage() {
  const mutation = useArtworkJob("/admin/artwork/purge", "Artwork storage plan queued");
  return {
    ...mutation,
    mutate: (request: ArtworkPurgeRequest, options?: Parameters<typeof mutation.mutate>[1]) =>
      mutation.mutate(request, options),
    mutateAsync: (request: ArtworkPurgeRequest) => mutation.mutateAsync(request),
  };
}
