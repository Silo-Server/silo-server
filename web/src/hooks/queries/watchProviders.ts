import { useEffect, useRef } from "react";
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext } from "@/api/client";
import { v2, V2ProblemError } from "@/api/v2/request";
import { favoriteKeys, watchlistKeys, watchProviderKeys } from "./keys";
import { invalidateAllRatingSurfaceQueries } from "./ratingsSurfaceRefresh";
import { toast } from "sonner";
import { storage } from "@/utils/storage";
import type { PluginConfigSchema } from "@/api/types";

export type WatchProviderConnectionConfig = Record<string, Record<string, unknown>>;

export interface WatchProviderSummary {
  key: string;
  display_name: string;
  capabilities: WatchProviderCapabilities;
  connection_config_schema?: PluginConfigSchema[];
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
  import_ratings: boolean;
  export_ratings: boolean;
}

export interface WatchProviderConnection {
  etag?: string;
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
  import_ratings_enabled: boolean;
  export_ratings_enabled: boolean;
  credentials_configured: boolean;
  connection_config_schema?: PluginConfigSchema[];
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
  inbound_ratings_found: number;
  inbound_ratings_imported: number;
  outbound_ratings_found: number;
  outbound_ratings_sent: number;
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
    | "import_ratings_enabled"
    | "export_ratings_enabled"
  >
>;

export async function fetchWatchProviders() {
  const result = await v2("GET /api/v2/watch-providers");
  return { providers: result.items as WatchProviderSummary[] };
}

export async function fetchWatchProviderConnection(provider: string) {
  const profileContext = captureProfileRequestContext();
  const scope = profileContext ? { profileContext } : {};
  const result = await v2("GET /api/v2/watch-providers/{provider}/connection", {
    path: { provider },
    ...scope,
  });
  if (!result.connected) return result as WatchProviderConnection;
  let etag: string | undefined;
  const settings = await v2("GET /api/v2/watch-providers/{provider}/connection/settings", {
    path: { provider },
    ...scope,
    onResponse: (response) => {
      etag = response.headers.get("ETag") ?? undefined;
    },
  });
  return { ...result, ...settings, etag } as WatchProviderConnection;
}

export function startWatchProviderDeviceAuth(provider: string) {
  return v2("POST /api/v2/watch-providers/{provider}/auth/device-code", { path: { provider } });
}

export async function pollWatchProviderDeviceAuth(provider: string, authSessionId: string) {
  const result = await v2("POST /api/v2/watch-providers/{provider}/auth/poll", {
    path: { provider },
    body: { auth_session_id: authSessionId },
  });
  return result as WatchProviderConnection;
}

export async function connectWatchProviderAPIKey(
  provider: string,
  apiKey: string,
  connectionConfig: WatchProviderConnectionConfig = {},
) {
  const result = await v2("POST /api/v2/watch-providers/{provider}/auth/api-key", {
    path: { provider },
    body: { api_key: apiKey, connection_config: connectionConfig },
  });
  return result as WatchProviderConnection;
}

export async function updateWatchProviderConnection(
  provider: string,
  body: UpdateWatchProviderConnection,
  expectedETag: string,
) {
  const profileContext = captureProfileRequestContext();
  let etag: string | undefined;
  const result = await v2("PATCH /api/v2/watch-providers/{provider}/connection", {
    path: { provider },
    ...(profileContext ? { profileContext } : {}),
    onResponse: (response) => {
      etag = response.headers.get("ETag") ?? undefined;
    },
    body,
    headers: { "If-Match": expectedETag },
  });
  return { ...result, etag };
}

export function deleteWatchProviderConnection(provider: string) {
  return v2("DELETE /api/v2/watch-providers/{provider}/connection", { path: { provider } });
}

export function triggerWatchProviderSync(provider: string) {
  return v2("POST /api/v2/watch-providers/{provider}/sync", { path: { provider } });
}

export async function fetchWatchProviderSyncRuns(provider: string) {
  const result = await v2("GET /api/v2/watch-providers/{provider}/sync-runs", {
    path: { provider },
    query: { limit: 10 },
  });
  return { runs: result.items as WatchProviderSyncRun[] };
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
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: watchProviderKeys.syncRuns(profileId, provider),
    queryFn: () => fetchWatchProviderSyncRuns(provider),
    enabled: enabled && Boolean(profileId),
    refetchInterval: (query) => {
      const latest = query.state.data?.runs?.[0];
      return latest && isActiveSyncRun(latest) ? 4_000 : false;
    },
  });
  // A sync imports favorites, watchlist entries and ratings while it runs, so
  // the surfaces showing them refresh once the run this page watched finishes.
  const latest = query.data?.runs?.[0];
  const observed = useRef<WatchProviderSyncRun | undefined>(undefined);
  useEffect(() => {
    if (!latest) return;
    const previous = observed.current;
    observed.current = latest;
    if (syncRunFinished(previous, latest)) {
      void invalidateSyncedSurfaces(queryClient);
    }
  }, [latest, queryClient]);
  return query;
}

function isActiveSyncRun(run: WatchProviderSyncRun) {
  return run.status === "queued" || run.status === "running";
}

// syncRunFinished reports whether latest is a run that finished since previous
// was seen: either the run previously seen as queued or running, or a newer
// run already final when first seen, such as a scheduled run that started and
// ended between two reads. The first read of the page is never a completion.
export function syncRunFinished(
  previous: WatchProviderSyncRun | undefined,
  latest: WatchProviderSyncRun,
) {
  if (previous === undefined || isActiveSyncRun(latest)) return false;
  return previous.id === latest.id ? isActiveSyncRun(previous) : true;
}

async function invalidateSyncedSurfaces(queryClient: QueryClient) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: favoriteKeys.all }),
    queryClient.invalidateQueries({ queryKey: watchlistKeys.list() }),
    invalidateAllRatingSurfaceQueries(queryClient),
  ]);
}

export function useStartWatchProviderDeviceAuth(provider: string) {
  return useMutation({
    retry: false,
    mutationFn: () => startWatchProviderDeviceAuth(provider),
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to start auth"),
  });
}

export function usePollWatchProviderDeviceAuth(provider: string) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (authSessionId: string) => pollWatchProviderDeviceAuth(provider, authSessionId),
    onSuccess: (connection) => {
      const profileId = getActiveProfileId();
      queryClient.setQueryData(watchProviderKeys.connection(profileId, provider), connection);
      queryClient.invalidateQueries({
        queryKey: watchProviderKeys.connection(profileId, provider),
      });
      toast.success("Watch provider connected");
    },
    onError: (err) => toast.error(err instanceof Error ? err.message : "Failed to finish auth"),
  });
}

export function useConnectWatchProviderAPIKey(provider: string) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      apiKey,
      connectionConfig,
    }: {
      apiKey: string;
      connectionConfig?: WatchProviderConnectionConfig;
    }) => connectWatchProviderAPIKey(provider, apiKey, connectionConfig),
    onSuccess: (connection) => {
      const profileId = getActiveProfileId();
      queryClient.setQueryData(watchProviderKeys.connection(profileId, provider), connection);
      queryClient.invalidateQueries({
        queryKey: watchProviderKeys.connection(profileId, provider),
      });
      toast.success("Watch provider connected");
    },
    onError: (err) =>
      toast.error(err instanceof Error ? err.message : "Failed to connect provider"),
  });
}

export function useUpdateWatchProviderConnection(provider: string) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    onMutate: () => ({ profileId: getActiveProfileId() }),
    mutationFn: (body: UpdateWatchProviderConnection) => {
      const current = queryClient.getQueryData<WatchProviderConnection>(
        watchProviderKeys.connection(getActiveProfileId(), provider),
      );
      if (!current?.etag) throw new Error("Reload provider settings before saving changes.");
      return updateWatchProviderConnection(provider, body, current.etag);
    },
    onSuccess: (settings, _variables, context) => {
      queryClient.setQueryData<WatchProviderConnection>(
        watchProviderKeys.connection(context?.profileId, provider),
        (current) => (current ? { ...current, ...settings } : undefined),
      );
    },
    onError: async (err, _variables, context) => {
      if (err instanceof V2ProblemError && err.status === 412) {
        await queryClient.invalidateQueries({
          queryKey: watchProviderKeys.connection(context?.profileId, provider),
        });
        return;
      }
      toast.error(err instanceof Error ? err.message : "Failed to update provider");
    },
  });
}

export function useDeleteWatchProviderConnection(provider: string) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
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
    retry: false,
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
      queryClient.invalidateQueries({ queryKey: favoriteKeys.all });
      queryClient.invalidateQueries({ queryKey: watchlistKeys.list() });
      toast.success("Watch provider sync started");
    },
    onError: (err) => {
      if (err instanceof V2ProblemError && err.status === 429) {
        const retryAfter = err.retryAfterSeconds;
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
