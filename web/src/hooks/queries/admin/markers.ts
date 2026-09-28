import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { getAllMarkerHistory } from "@/api/v2/markers";
import { v2 } from "@/api/v2/request";
import type { MarkerProviderUpdateRequest } from "@/api/types";
import { useAdminServerSettings } from "@/hooks/queries/admin/settings";
import { adminKeys } from "@/hooks/queries/keys";

const ADMIN_STALE_TIME = 30_000;

/**
 * Reads the marker analysis this API build supports. During a rolling deploy
 * the web bundle can be newer than the node answering, so callers gate newer
 * marker actions on it and treat a failed read as "not supported".
 */
export function useAdminMarkerCapabilities(enabled = true) {
  return useQuery({
    queryKey: adminKeys.markerCapabilities(),
    queryFn: ({ signal }) => v2("GET /api/v2/admin/markers/capabilities", { signal }),
    staleTime: ADMIN_STALE_TIME,
    retry: false,
    enabled,
  });
}

/** Which marker kinds local detection finds on this server. */
export interface MarkerDetectionKinds {
  intro: boolean;
  credits: boolean;
}

/**
 * Reads the markers.detect_intros and markers.detect_credits settings. It
 * returns undefined until they load, when they cannot be read, and on an API
 * node that ignores them, so callers then offer every kind and leave the
 * decision to the server. A failed refetch counts as unreadable: the cached
 * answer may describe another node or an outdated setting.
 */
export function useMarkerDetectionKinds(enabled = true): MarkerDetectionKinds | undefined {
  const capabilities = useAdminMarkerCapabilities(enabled);
  const honorsSettings =
    !capabilities.isError && capabilities.data?.detection_kind_settings === true;
  const settings = useAdminServerSettings({ enabled: enabled && honorsSettings });
  if (!honorsSettings || settings.isError || !settings.data) return undefined;
  return {
    intro: detectionToggleEnabled(settings.data["markers.detect_intros"]),
    credits: detectionToggleEnabled(settings.data["markers.detect_credits"]),
  };
}

// Only an explicit false turns a kind off, as on the server, so a server that
// never saved the setting keeps detecting.
function detectionToggleEnabled(raw: string | undefined): boolean {
  return raw?.trim().toLowerCase() !== "false";
}

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
