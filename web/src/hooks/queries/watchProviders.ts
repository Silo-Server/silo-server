import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiClientError } from "@/api/client";
import { favoriteKeys, watchlistKeys, watchProviderKeys } from "./keys";
import { toast } from "sonner";
import { storage } from "@/utils/storage";

export interface WatchProviderSummary {
  key: string;
  display_name: string;
  capabilities: WatchProviderCapabilities;
}

export const WatchProviderAuthMethod = {
  DeviceCode: "device_code",
  APIKey: "api_key",
} as const;
export type WatchProviderAuthMethod =
  (typeof WatchProviderAuthMethod)[keyof typeof WatchProviderAuthMethod];

export interface WatchProviderCapabilities {
  import_watched: boolean;
  import_progress: boolean;
  export_watched: boolean;
  export_unwatched: boolean;
  import_favorites: boolean;
  export_favorites: boolean;
  remove_favorites: boolean;
  import_watchlist: boolean;
  export_watchlist: boolean;
  remove_watchlist: boolean;
  provides_watchlist_order: boolean;
  scrobble_playback: boolean;
}

export interface WatchProviderConnection {
  provider: string;
  display_name: string;
  capabilities: WatchProviderCapabilities;
  auth_method: WatchProviderAuthMethod;
  connected: boolean;
  provider_username?: string;
  import_watched_enabled: boolean;
  import_progress_enabled: boolean;
  export_watched_enabled: boolean;
  export_unwatched_enabled: boolean;
  import_favorites_enabled: boolean;
  export_favorites_enabled: boolean;
  sync_favorite_removals_enabled: boolean;
  import_watchlist_enabled: boolean;
  export_watchlist_enabled: boolean;
  sync_watchlist_removals_enabled: boolean;
  sync_watchlist_order_enabled: boolean;
  scrobble_enabled: boolean;
  credentials_configured: boolean;
  last_inbound_sync_at?: string;
  last_progress_sync_at?: string;
  last_outbound_sync_at?: string;
  last_favorites_sync_at?: string;
  last_watchlist_sync_at?: string;
  last_scrobble_error_at?: string;
  last_error?: string;
}

export interface DeviceAuthSession {
  id: string;
  provider: string;
  user_code: string;
  verification_url: string;
  interval_seconds: number;
  expires_at: string;
}

export interface WatchProviderSyncRun {
  id: string;
  connection_id: string;
  trigger: "manual" | "scheduled";
  status: "queued" | "running" | "success" | "warning" | "failed";
  provider: string;
  inbound_watched_found: number;
  inbound_watched_imported: number;
  inbound_progress_found: number;
  inbound_progress_imported: number;
  outbound_found: number;
  outbound_sent: number;
  inbound_favorites_found: number;
  inbound_favorites_imported: number;
  outbound_favorites_found: number;
  outbound_favorites_sent: number;
  favorite_removals_sent: number;
  inbound_watchlist_found: number;
  inbound_watchlist_imported: number;
  outbound_watchlist_found: number;
  outbound_watchlist_sent: number;
  watchlist_removals_sent: number;
  warning?: string;
  error?: string;
  started_at: string;
  completed_at?: string;
  created_at: string;
}

export interface WatchProviderManualSyncResponse {
  run: WatchProviderSyncRun;
  retry_after_seconds: number;
}

type DeviceAuthResponse = DeviceAuthSession & {
  ID?: string;
  Provider?: string;
  UserCode?: string;
  VerificationURL?: string;
  IntervalSeconds?: number;
  ExpiresAt?: string;
};

export type UpdateWatchProviderConnection = Partial<
  Pick<
    WatchProviderConnection,
    | "import_watched_enabled"
    | "import_progress_enabled"
    | "export_watched_enabled"
    | "export_unwatched_enabled"
    | "import_favorites_enabled"
    | "export_favorites_enabled"
    | "sync_favorite_removals_enabled"
    | "import_watchlist_enabled"
    | "export_watchlist_enabled"
    | "sync_watchlist_removals_enabled"
    | "sync_watchlist_order_enabled"
    | "scrobble_enabled"
  >
>;

export function fetchWatchProviders() {
  return api<{ providers: WatchProviderSummary[] }>("/watch-providers");
}

export function fetchWatchProviderConnection(provider: string) {
  return api<WatchProviderConnection>(`/watch-providers/${provider}/connection`);
}

export function startWatchProviderDeviceAuth(provider: string) {
  return api<DeviceAuthResponse>(`/watch-providers/${provider}/auth/device-code`, {
    method: "POST",
  }).then((session) => ({
    id: session.id ?? session.ID ?? "",
    provider: session.provider ?? session.Provider ?? provider,
    user_code: session.user_code ?? session.UserCode ?? "",
    verification_url: session.verification_url ?? session.VerificationURL ?? "",
    interval_seconds: session.interval_seconds ?? session.IntervalSeconds ?? 0,
    expires_at: session.expires_at ?? session.ExpiresAt ?? "",
  }));
}

export function pollWatchProviderDeviceAuth(provider: string, authSessionId: string) {
  return api<WatchProviderConnection>(`/watch-providers/${provider}/auth/poll`, {
    method: "POST",
    body: JSON.stringify({ auth_session_id: authSessionId }),
  });
}

export function connectWatchProviderAPIKey(provider: string, apiKey: string) {
  return api<WatchProviderConnection>(`/watch-providers/${provider}/auth/api-key`, {
    method: "POST",
    body: JSON.stringify({ api_key: apiKey }),
  });
}

export function updateWatchProviderConnection(
  provider: string,
  body: UpdateWatchProviderConnection,
) {
  return api<WatchProviderConnection>(`/watch-providers/${provider}/connection`, {
    method: "PATCH",
    body: JSON.stringify(body),
  });
}

export function deleteWatchProviderConnection(provider: string) {
  return api(`/watch-providers/${provider}/connection`, { method: "DELETE" });
}

export function triggerWatchProviderSync(provider: string) {
  return api<WatchProviderManualSyncResponse>(`/watch-providers/${provider}/sync`, {
    method: "POST",
  });
}

export function fetchWatchProviderSyncRuns(provider: string) {
  return api<{ runs: WatchProviderSyncRun[] }>(`/watch-providers/${provider}/sync-runs`);
}

function getActiveProfileId() {
  return storage.get(storage.KEYS.PROFILE_ID);
}

export function useWatchProviders() {
  const profileId = getActiveProfileId();
  return useQuery({
    queryKey: watchProviderKeys.providers(profileId),
    queryFn: fetchWatchProviders,
    enabled: Boolean(profileId),
  });
}

export function useWatchProviderConnection(provider: string) {
  const profileId = getActiveProfileId();
  return useQuery({
    queryKey: watchProviderKeys.connection(profileId, provider),
    queryFn: () => fetchWatchProviderConnection(provider),
    enabled: Boolean(profileId),
  });
}

export function useWatchProviderSyncRuns(provider: string, enabled = true) {
  const profileId = getActiveProfileId();
  return useQuery({
    queryKey: watchProviderKeys.syncRuns(profileId, provider),
    queryFn: () => fetchWatchProviderSyncRuns(provider),
    enabled: enabled && Boolean(profileId),
    refetchInterval: (query) => {
      const latest = query.state.data?.runs?.[0];
      return latest?.status === "queued" || latest?.status === "running" ? 4_000 : false;
    },
  });
}

export function useStartWatchProviderDeviceAuth(provider: string) {
  return useMutation({
    mutationFn: () => startWatchProviderDeviceAuth(provider),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to start auth"),
  });
}

export function usePollWatchProviderDeviceAuth(provider: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (authSessionId: string) => pollWatchProviderDeviceAuth(provider, authSessionId),
    onSuccess: (connection) => {
      const profileId = getActiveProfileId();
      queryClient.setQueryData(watchProviderKeys.connection(profileId, provider), connection);
      toast.success("Watch provider connected");
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to finish auth"),
  });
}

export function useConnectWatchProviderAPIKey(provider: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (apiKey: string) => connectWatchProviderAPIKey(provider, apiKey),
    onSuccess: (connection) => {
      const profileId = getActiveProfileId();
      queryClient.setQueryData(watchProviderKeys.connection(profileId, provider), connection);
      toast.success("Watch provider connected");
    },
    onError: (err) =>
      toast.error(err instanceof Error ? err.message : "Failed to connect provider"),
  });
}

export function useUpdateWatchProviderConnection(provider: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateWatchProviderConnection) =>
      updateWatchProviderConnection(provider, body),
    onSuccess: (connection) => {
      const profileId = getActiveProfileId();
      queryClient.setQueryData(watchProviderKeys.connection(profileId, provider), connection);
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to update provider"),
  });
}

export function useDeleteWatchProviderConnection(provider: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => deleteWatchProviderConnection(provider),
    onSuccess: () => {
      const profileId = getActiveProfileId();
      queryClient.invalidateQueries({
        queryKey: watchProviderKeys.connection(profileId, provider),
      });
      toast.success("Watch provider disconnected");
    },
    onError: (err) =>
      toast.error(err instanceof Error ? err.message : "Failed to disconnect provider"),
  });
}

export function useTriggerWatchProviderSync(provider: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => triggerWatchProviderSync(provider),
    onSuccess: (response) => {
      const profileId = getActiveProfileId();
      queryClient.setQueryData(watchProviderKeys.syncRuns(profileId, provider), {
        runs: [response.run],
      });
      queryClient.invalidateQueries({ queryKey: watchProviderKeys.syncRuns(profileId, provider) });
      queryClient.invalidateQueries({
        queryKey: watchProviderKeys.connection(profileId, provider),
      });
      queryClient.invalidateQueries({ queryKey: favoriteKeys.list() });
      queryClient.invalidateQueries({ queryKey: watchlistKeys.list() });
      toast.success("Watch provider sync started");
    },
    onError: (err) => {
      if (err instanceof ApiClientError && err.status === 429) {
        const retryAfter = err.details?.retry_after_seconds;
        toast.error(
          retryAfter ? `Sync available in ${formatRetryAfter(retryAfter)}` : "Sync is cooling down",
        );
        return;
      }
      toast.error(err instanceof Error ? err.message : "Failed to start sync");
    },
  });
}

function formatRetryAfter(seconds: number) {
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.ceil(seconds / 60);
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  return remainingMinutes > 0 ? `${hours}h ${remainingMinutes}m` : `${hours}h`;
}
