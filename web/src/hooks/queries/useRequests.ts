import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import type {
  CreateMediaRequestInput,
  DiscoverBrowseKind,
  DiscoverBrowseResponse,
  DiscoverGenresResponse,
  DiscoverNetworksResponse,
  DiscoverStudiosResponse,
  LoadRequestIntegrationOptionsRequest,
  MediaRequest,
  MediaRequestsListResponse,
  RequestDiscoveryResponse,
  RequestDiscoverySection,
  RequestFeatureStatus,
  RequestIntegration,
  RequestIntegrationOptions,
  RequestIntegrationsResponse,
  RequestListParams,
  RequestMediaDetail,
  RequestMediaPage,
  RequestSearchMediaType,
  RequestMediaType,
  RequestSettings,
  RequestUserLimit,
} from "@/api/types";
import { adminKeys, requestKeys } from "./keys";

const REQUESTS_STALE_TIME = 30_000;
const DISCOVER_BRAND_STALE_TIME = 24 * 60 * 60 * 1000;
const BROWSE_STALE_TIME = 60 * 1000;

function listParamsKey(params: RequestListParams) {
  return {
    status: params.status ?? "all",
    outcome: params.outcome ?? "all",
    limit: params.limit ?? 50,
    offset: params.offset ?? 0,
  };
}

function buildListQuery(params: RequestListParams = {}) {
  const query = new URLSearchParams();
  if (params.status && params.status !== "all") query.set("status", params.status);
  if (params.outcome && params.outcome !== "all") query.set("outcome", params.outcome);
  if (params.limit != null && params.limit > 0) query.set("limit", String(params.limit));
  if (params.offset != null && params.offset > 0) query.set("offset", String(params.offset));
  const encoded = query.toString();
  return encoded ? `?${encoded}` : "";
}

function invalidateRequestSurfaces(queryClient: ReturnType<typeof useQueryClient>) {
  // requestKeys.all = ["requests"], so invalidating it cascades to nested keys,
  // including requestKeys.search(...). Policy mutations rely on this to refresh
  // viewer-scoped search results when request eligibility changes.
  queryClient.invalidateQueries({ queryKey: requestKeys.all });
  queryClient.invalidateQueries({ queryKey: adminKeys.requestsRoot() });
}

export function useRequestDiscovery() {
  return useQuery({
    queryKey: requestKeys.discovery(),
    queryFn: () =>
      api<RequestDiscoveryResponse>("/requests/discover").then((data) => data.sections ?? []),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useRequestFeatureStatus() {
  return useQuery({
    queryKey: requestKeys.status(),
    queryFn: () => api<RequestFeatureStatus>("/requests/status"),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useRequestDiscoverySection(section: string, page = 1) {
  return useQuery({
    queryKey: requestKeys.discoverySection(section, page),
    queryFn: () =>
      api<RequestDiscoverySection>(
        `/requests/discover/${encodeURIComponent(section)}?page=${page}`,
      ),
    enabled: section.trim().length > 0,
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useDiscoverStudios() {
  return useQuery({
    queryKey: requestKeys.discoverStudios(),
    queryFn: () =>
      api<DiscoverStudiosResponse>("/requests/discover/studios").then((data) => data.studios ?? []),
    staleTime: DISCOVER_BRAND_STALE_TIME,
  });
}

export function useDiscoverNetworks() {
  return useQuery({
    queryKey: requestKeys.discoverNetworks(),
    queryFn: () =>
      api<DiscoverNetworksResponse>("/requests/discover/networks").then(
        (data) => data.networks ?? [],
      ),
    staleTime: DISCOVER_BRAND_STALE_TIME,
  });
}

export function useDiscoverGenres() {
  return useQuery({
    queryKey: requestKeys.discoverGenres(),
    queryFn: () =>
      api<DiscoverGenresResponse>("/requests/discover/genres").then((data) => data.genres ?? []),
    staleTime: DISCOVER_BRAND_STALE_TIME,
  });
}

export interface UseRequestBrowseArgs {
  kind: DiscoverBrowseKind;
  slug: string;
  mediaType?: RequestMediaType;
  sort: "popularity" | "vote_average" | "release_date";
  page: number;
}

export function useRequestBrowse({ kind, slug, mediaType, sort, page }: UseRequestBrowseArgs) {
  return useQuery({
    queryKey: requestKeys.discoverBrowse(kind, slug, mediaType, sort, page),
    queryFn: () => {
      const params = new URLSearchParams({ sort, page: String(page) });
      if (mediaType) params.set("media_type", mediaType);
      return api<DiscoverBrowseResponse>(
        `/requests/discover/browse/${kind}/${encodeURIComponent(slug)}?${params}`,
      );
    },
    enabled: slug.trim().length > 0 && (kind !== "genre" || Boolean(mediaType)),
    staleTime: BROWSE_STALE_TIME,
  });
}

export function useRequestMediaDetail(mediaType: RequestMediaType, tmdbID: number) {
  return useQuery({
    queryKey: requestKeys.detail(mediaType, tmdbID),
    queryFn: () =>
      api<RequestMediaDetail>(
        `/requests/detail/${encodeURIComponent(mediaType)}/${encodeURIComponent(String(tmdbID))}`,
      ),
    enabled: tmdbID > 0,
    staleTime: REQUESTS_STALE_TIME,
  });
}

export interface UseRequestSearchOptions {
  /** When false, suppresses the query regardless of the query string. Default: true. */
  enabled?: boolean;
  /** When true, suppresses the query until the active profile is loaded. Default: false. */
  requireProfile?: boolean;
  /** Cache freshness window for this search surface. Default: existing Requests page timing. */
  staleTime?: number;
}

export function useRequestSearch(
  mediaType: RequestSearchMediaType,
  query: string,
  page = 1,
  options: UseRequestSearchOptions = {},
) {
  const normalizedQuery = query.trim();
  const { profile } = useCurrentProfile();
  // Use a sentinel viewerKey when there is no profile so the cache key is stable,
  // but suppress the actual fetch — see the `enabled` gate below. This prevents
  // any anonymous request results from being written into a bucket that could
  // later be read by a different viewer.
  const viewerKey = profile?.id ?? "anon";
  const enabledOverride = options.enabled ?? true;
  const requireProfile = options.requireProfile ?? false;

  return useQuery({
    queryKey: requestKeys.search(mediaType, normalizedQuery, page, viewerKey),
    queryFn: ({ signal }) => {
      const params = new URLSearchParams({
        q: normalizedQuery,
        media_type: mediaType,
        page: String(page),
      });
      return api<RequestMediaPage>(`/requests/search?${params}`, { signal });
    },
    enabled:
      enabledOverride && normalizedQuery.length > 1 && (!requireProfile || Boolean(profile?.id)),
    staleTime: options.staleTime ?? REQUESTS_STALE_TIME,
  });
}

export function useCreateMediaRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateMediaRequestInput) =>
      api<MediaRequest>("/requests/", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Request submitted");
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to submit request");
    },
  });
}

export function useMyMediaRequests(params: RequestListParams = {}) {
  const key = listParamsKey(params);
  return useQuery({
    queryKey: requestKeys.mine(key),
    queryFn: () =>
      api<MediaRequestsListResponse>(`/requests/mine${buildListQuery(params)}`).then(
        (data) => data.requests ?? [],
      ),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useAdminMediaRequests(params: RequestListParams = {}) {
  const key = listParamsKey(params);
  return useQuery({
    queryKey: adminKeys.requests(key),
    queryFn: () =>
      api<MediaRequestsListResponse>(`/admin/requests${buildListQuery(params)}`).then(
        (data) => data.requests ?? [],
      ),
    staleTime: 10_000,
  });
}

export function useApproveMediaRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api<MediaRequest>(`/admin/requests/${encodeURIComponent(id)}/approve`, {
        method: "POST",
      }),
    onSuccess: () => {
      toast.success("Request approved");
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to approve request");
    },
  });
}

export function useDeclineMediaRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, reason }: { id: string; reason?: string }) =>
      api<MediaRequest>(`/admin/requests/${encodeURIComponent(id)}/decline`, {
        method: "POST",
        body: JSON.stringify({ reason }),
      }),
    onSuccess: () => {
      toast.success("Request declined");
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to decline request");
    },
  });
}

export function useRetryMediaRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api<MediaRequest>(`/admin/requests/${encodeURIComponent(id)}/retry`, {
        method: "POST",
      }),
    onSuccess: () => {
      toast.success("Request queued for retry");
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to retry request");
    },
  });
}

export function useRequestSettings() {
  return useQuery({
    queryKey: adminKeys.requestSettings(),
    queryFn: () => api<RequestSettings>("/admin/request-settings"),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useUpdateRequestSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: RequestSettings) =>
      api<RequestSettings>("/admin/request-settings", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Request settings saved");
      queryClient.invalidateQueries({ queryKey: adminKeys.requestSettings() });
      queryClient.invalidateQueries({ queryKey: requestKeys.status() });
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save request settings");
    },
  });
}

export function useRequestIntegrations() {
  return useQuery({
    queryKey: adminKeys.requestIntegrations(),
    queryFn: () =>
      api<RequestIntegrationsResponse>("/admin/request-integrations").then(
        (data) => data.integrations ?? [],
      ),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useCreateRequestIntegration() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (integration: RequestIntegration) =>
      api<RequestIntegration>("/admin/request-integrations", {
        method: "POST",
        body: JSON.stringify(integration),
      }),
    onSuccess: () => {
      toast.success("Integration created");
      queryClient.invalidateQueries({ queryKey: adminKeys.requestIntegrations() });
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to create integration");
    },
  });
}

export function useUpdateRequestIntegration() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...integration }: RequestIntegration) =>
      api<RequestIntegration>(`/admin/request-integrations/${encodeURIComponent(id)}`, {
        method: "PUT",
        body: JSON.stringify({ id, ...integration }),
      }),
    onSuccess: () => {
      toast.success("Integration saved");
      queryClient.invalidateQueries({ queryKey: adminKeys.requestIntegrations() });
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save integration");
    },
  });
}

export function useDeleteRequestIntegration() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api<void>(`/admin/request-integrations/${encodeURIComponent(id)}`, {
        method: "DELETE",
      }),
    onSuccess: () => {
      toast.success("Integration deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.requestIntegrations() });
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete integration");
    },
  });
}

export function useLoadRequestIntegrationOptions() {
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: LoadRequestIntegrationOptionsRequest }) =>
      api<RequestIntegrationOptions>(
        `/admin/request-integrations/${encodeURIComponent(id)}/options`,
        {
          method: "POST",
          body: JSON.stringify(body),
        },
      ),
    onSuccess: () => {
      toast.success("Connection successful");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to load integration settings");
    },
  });
}

export function useRequestUserLimit(userId?: number) {
  return useQuery({
    queryKey: adminKeys.requestUserLimit(userId ?? 0),
    queryFn: () => api<RequestUserLimit>(`/admin/request-users/${userId}/limit`),
    enabled: Boolean(userId && userId > 0),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useUpdateRequestUserLimit() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, body }: { userId: number; body: RequestUserLimit }) =>
      api<RequestUserLimit>(`/admin/request-users/${userId}/limit`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: (_data, variables) => {
      toast.success("User request limit saved");
      queryClient.invalidateQueries({ queryKey: adminKeys.requestUserLimit(variables.userId) });
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save user limit");
    },
  });
}
