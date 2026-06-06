import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type {
  MarkerProviderConfig,
  MarkerProviderListResponse,
  MarkerProviderUpdateRequest,
  MarkerProviderValidationResponse,
} from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";

const ADMIN_STALE_TIME = 30_000;

export function useMarkerProviders() {
  return useQuery({
    queryKey: adminKeys.markerProviders(),
    queryFn: () =>
      api<MarkerProviderListResponse>("/admin/markers/providers").then(
        (data) => data ?? { providers: [] },
      ),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useUpdateMarkerProvider() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: ({ provider, patch }: { provider: string; patch: MarkerProviderUpdateRequest }) =>
      api<MarkerProviderConfig>(`/admin/markers/providers/${encodeURIComponent(provider)}`, {
        method: "PUT",
        body: JSON.stringify(patch),
      }),
    onSuccess: async (_data, variables) => {
      toast.success("Marker provider settings saved");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.markerProviders() }),
        queryClient.invalidateQueries({
          queryKey: adminKeys.markerProvider(variables.provider),
        }),
      ]);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save marker provider settings");
    },
  });
}

export function useValidateMarkerProvider() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (provider: string) =>
      api<MarkerProviderValidationResponse>(
        `/admin/markers/providers/${encodeURIComponent(provider)}/validate`,
        {
          method: "POST",
        },
      ),
    onSuccess: (data, provider) => {
      queryClient.setQueryData(adminKeys.markerProviderValidation(provider), data);
      if (data.valid) {
        toast.success("TheIntroDB key validated");
      } else {
        toast.error(data.error || "TheIntroDB validation failed");
      }
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "TheIntroDB validation failed");
    },
  });
}
