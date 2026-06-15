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
import { adminKeys, themeKeys } from "../keys";
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
    onSuccess: async (_data, variables) => {
      const invalidations = [
        queryClient.invalidateQueries({ queryKey: adminKeys.serverSettings() }),
        queryClient.invalidateQueries({
          queryKey: [...adminKeys.serverSettings(), "sensitive-status"] as const,
        }),
      ];
      if (variables.key.startsWith("jellyfin_compat.")) {
        invalidations.push(
          queryClient.invalidateQueries({ queryKey: adminKeys.jellyfinCompatStatus() }),
        );
      }
      // Branding and admin theme settings are served live by public endpoints
      // (`/theme/branding`, `/theme/admin-css`) and require no restart. Refresh
      // those caches so saved changes apply immediately instead of waiting out
      // the 60s / 5min stale windows.
      if (variables.key.startsWith("branding.") || variables.key.startsWith("ui.admin_")) {
        invalidations.push(
          queryClient.invalidateQueries({ queryKey: themeKeys.adminCss() }),
          queryClient.invalidateQueries({ queryKey: themeKeys.branding() }),
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
