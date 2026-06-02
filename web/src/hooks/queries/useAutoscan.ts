import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type {
  AutoscanSettings,
  AutoscanSource,
  AutoscanSourcesResponse,
  AutoscanPathRewrite,
  AutoscanRewriteSuggestions,
} from "@/api/types";
import { adminKeys } from "./keys";

const AUTOSCAN_STALE_TIME = 30_000;

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

export function useAutoscanSources() {
  return useQuery({
    queryKey: adminKeys.autoscanSources(),
    queryFn: () =>
      api<AutoscanSourcesResponse>("/admin/autoscan/sources").then((data) => data.sources ?? []),
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

export function useUpdateAutoscanSource() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      body,
    }: {
      id: string;
      body: { enabled: boolean; path_rewrites: AutoscanPathRewrite[] };
    }) =>
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
