import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type {
  AutoscanAvailableSourcesResponse,
  AutoscanConnection,
  AutoscanConnectionInput,
  AutoscanConnectionsResponse,
  AutoscanConnectionTestInput,
  AutoscanConnectionTestResult,
  AutoscanRewriteSuggestions,
  AutoscanSettings,
  AutoscanSource,
  AutoscanSourceCreateInput,
  AutoscanSourceInput,
  AutoscanSourcesResponse,
  AutoscanStatus,
} from "@/api/types";
import { adminKeys } from "./keys";

const AUTOSCAN_STALE_TIME = 30_000;

// --- Settings ---

export function useAutoscanSettings() {
  return useQuery({
    queryKey: adminKeys.autoscanSettings(),
    queryFn: () => api<AutoscanSettings>("/admin/autoscan/settings"),
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

export function useUpdateAutoscanSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: AutoscanSettings) =>
      api<AutoscanSettings>("/admin/autoscan/settings", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Autoscan settings saved");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSettings() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save autoscan settings");
    },
  });
}

// --- Connections ---

export function useAutoscanConnections() {
  return useQuery({
    queryKey: adminKeys.autoscanConnections(),
    queryFn: () =>
      api<AutoscanConnectionsResponse>("/admin/autoscan/connections").then(
        (data) => data.connections ?? [],
      ),
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

export function useCreateAutoscanConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: AutoscanConnectionInput) =>
      api<AutoscanConnection>("/admin/autoscan/connections", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Autoscan connection created");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanConnections() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to create autoscan connection");
    },
  });
}

export function useUpdateAutoscanConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: AutoscanConnectionInput }) =>
      api<AutoscanConnection>(`/admin/autoscan/connections/${encodeURIComponent(id)}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Autoscan connection updated");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanConnections() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update autoscan connection");
    },
  });
}

export function useDeleteAutoscanConnection() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api<void>(`/admin/autoscan/connections/${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      toast.success("Autoscan connection deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanConnections() });
      // Sources may have lost their connection binding; invalidate them too.
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete autoscan connection");
    },
  });
}

// --- Sources ---

export function useAutoscanSources() {
  return useQuery({
    queryKey: adminKeys.autoscanSources(),
    queryFn: () =>
      api<AutoscanSourcesResponse>("/admin/autoscan/sources").then((data) => data.sources ?? []),
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

export function useAvailableScanSources() {
  return useQuery({
    queryKey: adminKeys.autoscanScanSourcePlugins(),
    queryFn: () =>
      api<AutoscanAvailableSourcesResponse>("/admin/autoscan/scan-source-plugins").then(
        (data) => data.plugins ?? [],
      ),
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

export function useCreateAutoscanSource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: AutoscanSourceCreateInput) =>
      api<AutoscanSource>("/admin/autoscan/sources", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Autoscan source created");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to create autoscan source");
    },
  });
}

export function useUpdateAutoscanSource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: AutoscanSourceInput }) =>
      api<AutoscanSource>(`/admin/autoscan/sources/${encodeURIComponent(id)}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Autoscan source saved");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save autoscan source");
    },
  });
}

export function useDeleteAutoscanSource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api<void>(`/admin/autoscan/sources/${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      toast.success("Autoscan source deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete autoscan source");
    },
  });
}

/**
 * Test an arr connection. Accepts either an existing connection id, or raw
 * credentials (base_url + api_key_ref) / a request integration id for an
 * unsaved dialog. Returns the result so the caller can render it inline;
 * errors are surfaced via the returned result, not a toast (advisory only).
 */
export function useTestAutoscanConnection() {
  return useMutation({
    mutationFn: (body: AutoscanConnectionTestInput) =>
      api<AutoscanConnectionTestResult>("/admin/autoscan/connections/test", {
        method: "POST",
        body: JSON.stringify(body),
      }),
  });
}

/**
 * Lazily fetch rewrite suggestions for a single source. Triggered on demand
 * (per source) so it is modelled as a mutation rather than a query. Returns
 * the suggestions; the 400 (no bound connection) surfaces as a toast.
 */
export function useAutoscanRewriteSuggestions() {
  return useMutation({
    mutationFn: (id: string) =>
      api<AutoscanRewriteSuggestions>(
        `/admin/autoscan/sources/${encodeURIComponent(id)}/rewrite-suggestions`,
      ),
    onError: (err) =>
      toast.error(
        err instanceof Error ? err.message : "Could not sync rewrites from the arr instance",
      ),
  });
}

// --- Status ---

export function useAutoscanStatus() {
  return useQuery({
    queryKey: adminKeys.autoscanStatus(),
    queryFn: () => api<AutoscanStatus>("/admin/autoscan/status"),
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

// --- Trigger ---

export function useTriggerAutoscan() {
  return useMutation({
    mutationFn: () =>
      api<{ status: string }>("/admin/autoscan/trigger", {
        method: "POST",
      }),
    onSuccess: () => {
      toast.success("Autoscan triggered");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to trigger autoscan");
    },
  });
}
