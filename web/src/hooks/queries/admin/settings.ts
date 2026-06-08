import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type {
  AdminSettingUpdateResponse,
  AdminSettingsConnectionCheckRequest,
  ConnectionCheckResponse,
  JellyfinCompatSettingsPatch,
  JellyfinCompatStatus,
  JellyfinCompatWebInstallRequest,
} from "@/api/types";
import { adminKeys } from "../keys";
import { toast } from "sonner";

type ServerSettings = Record<string, string>;

interface SensitiveStatusResponse {
  configured: string[];
  managed_by_env?: string[];
}

export function useAdminServerSettings() {
  return useQuery({
    queryKey: adminKeys.serverSettings(),
    queryFn: () => api<ServerSettings>("/admin/settings").then((d) => d ?? {}),
    staleTime: 30_000,
  });
}

export function useUpdateServerSetting() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ key, value }: { key: string; value: string }) =>
      api<AdminSettingUpdateResponse>(`/admin/settings/${key}`, {
        method: "PUT",
        body: JSON.stringify({ value }),
      }),
    onSuccess: async (_, variables) => {
      const invalidations = [
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
        queryClient.invalidateQueries({
          queryKey: [...adminKeys.serverSettings(), "sensitive-status"] as const,
        }),
        queryClient.invalidateQueries({ queryKey: adminKeys.serverStatus() }),
      ];
      if (variables.key.startsWith("jellyfin_compat.")) {
        invalidations.push(
          queryClient.invalidateQueries({ queryKey: adminKeys.jellyfinCompatStatus() }),
        );
      }
      await Promise.all(invalidations);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update setting");
    },
  });
}

export function useAdminSensitiveStatus() {
  return useQuery({
    queryKey: [...adminKeys.serverSettings(), "sensitive-status"] as const,
    queryFn: () => api<SensitiveStatusResponse>("/admin/settings/sensitive-status"),
    staleTime: 30_000,
  });
}

export function useCheckAdminSettingsConnection() {
  return useMutation({
    mutationFn: ({ kind, body }: { kind: string; body: AdminSettingsConnectionCheckRequest }) =>
      api<ConnectionCheckResponse>(`/admin/settings/check/${kind}`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
  });
}

export function useJellyfinCompatStatus() {
  return useQuery({
    queryKey: adminKeys.jellyfinCompatStatus(),
    queryFn: () => api<JellyfinCompatStatus>("/admin/jellyfin-compat/status"),
    staleTime: 15_000,
    refetchInterval: (query) => {
      const status = query.state.data;
      return status?.operation?.state === "running" ||
        status?.web_state === "installing" ||
        status?.web_state === "removing"
        ? 2_000
        : false;
    },
  });
}

export function useUpdateJellyfinCompatSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: JellyfinCompatSettingsPatch) =>
      api<JellyfinCompatStatus>("/admin/jellyfin-compat/settings", {
        method: "PATCH",
        body: JSON.stringify(body),
      }),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.jellyfinCompatStatus() }),
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
        queryClient.invalidateQueries({ queryKey: adminKeys.serverStatus() }),
      ]);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update Jellyfin compatibility");
    },
  });
}

export function useInstallJellyfinCompatWeb() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: JellyfinCompatWebInstallRequest = {}) =>
      api<JellyfinCompatStatus>("/admin/jellyfin-compat/web/install", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: async () => {
      toast.success("Jellyfin Web install started");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.jellyfinCompatStatus() }),
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
      ]);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to install Jellyfin Web assets");
    },
  });
}

export function useRemoveJellyfinCompatWeb() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api<JellyfinCompatStatus>("/admin/jellyfin-compat/web/remove", {
        method: "POST",
        body: JSON.stringify({}),
      }),
    onSuccess: async () => {
      toast.success("Jellyfin Web removal started");
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.jellyfinCompatStatus() }),
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
      ]);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove Jellyfin Web assets");
    },
  });
}
