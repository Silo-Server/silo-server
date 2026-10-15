import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { adminKeys } from "../keys";
import { toast } from "sonner";

export function useRecommendationsStatus() {
  return useQuery({
    queryKey: adminKeys.recommendationsStatus(),
    queryFn: () => v2("GET /api/v2/admin/recommendations/status"),
    refetchInterval: 5000,
  });
}

export function useTriggerEmbeddings() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: () =>
      v2("POST /api/v2/admin/recommendations/trigger/embeddings", { retryAuthentication: false }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.recommendationsStatus() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to trigger embeddings");
    },
  });
}

export function useTriggerTasteProfiles() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: () =>
      v2("POST /api/v2/admin/recommendations/trigger/taste-profiles", {
        retryAuthentication: false,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.recommendationsStatus() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to trigger taste profiles");
    },
  });
}

export function useTriggerCowatch() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: () =>
      v2("POST /api/v2/admin/recommendations/trigger/cowatch", { retryAuthentication: false }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.recommendationsStatus() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to trigger co-watch computation");
    },
  });
}

export function useTriggerRecommendations() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: () =>
      v2("POST /api/v2/admin/recommendations/trigger/recommendations", {
        retryAuthentication: false,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.recommendationsStatus() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to trigger recommendations");
    },
  });
}

/**
 * Deletes the embedding lock, every item embedding, taste profiles and
 * per-profile cached rows. Not retried: a later replay would also delete what
 * the embedding job built since. The caller reports success and failure.
 */
export function useResetEmbeddings() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: () =>
      v2("POST /api/v2/admin/recommendations/embeddings/reset", { retryAuthentication: false }),
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.recommendationsStatus() });
      // The lock card reads the lock from the stored settings.
      queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() });
    },
  });
}
