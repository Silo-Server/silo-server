import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { getAllMarkerHistory } from "@/api/v2/markers";
import { v2 } from "@/api/v2/request";
import type { MarkerProviderUpdateRequest } from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";

const ADMIN_STALE_TIME = 30_000;

export function useMarkerProviders() {
  return useQuery({
    queryKey: adminKeys.markerProviders(),
    queryFn: ({ signal }) => v2("GET /api/v2/admin/markers/providers", { signal }),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAllMarkerEditHistory(limit = 50) {
  return useQuery({
    queryKey: adminKeys.markerHistory(limit),
    queryFn: ({ signal }) => getAllMarkerHistory(limit, signal),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useUpdateMarkerProvider() {
  const queryClient = useQueryClient();

  return useMutation({
    retry: false,
    mutationFn: ({ provider, patch }: { provider: string; patch: MarkerProviderUpdateRequest }) =>
      v2("PUT /api/v2/admin/markers/providers/{provider}", {
        path: { provider },
        body: patch,
        retryAuthentication: false,
      }),
    onSuccess: async (_data, variables) => {
      toast.success("Marker provider settings saved");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.markerProviders() }),
        queryClient.invalidateQueries({
          queryKey: adminKeys.markerProvider(variables.provider),
        }),
        queryClient.removeQueries({
          queryKey: adminKeys.markerProviderValidation(variables.provider),
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
    retry: false,
    mutationFn: ({ provider }: { provider: string; displayName?: string }) =>
      v2("POST /api/v2/admin/markers/providers/{provider}/validate", {
        path: { provider },
        retryAuthentication: false,
      }),
    onSuccess: (data, variables) => {
      const label = variables.displayName || "Marker provider";
      const provider = variables.provider;
      queryClient.setQueryData(adminKeys.markerProviderValidation(provider), data);
      if (data.valid) {
        toast.success(`${label} connection successful`);
      } else {
        toast.error(data.error || `${label} connection test failed`);
      }
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Marker provider connection test failed");
    },
  });
}
