import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { V2ProblemError } from "@/api/v2/request";
import {
  getAdminRequestSettingsV2,
  putAdminRequestSettingsV2,
  getAdminRequestUserLimitV2,
  putAdminRequestUserLimitV2,
  listAdminRequestIntegrationsV2,
  saveAdminRequestIntegrationV2,
  deleteAdminRequestIntegrationV2,
  listAdminMediaRequestsV2,
  approveAdminRequestV2,
  declineAdminRequestV2,
  retryAdminRequestV2,
  loadAdminRequestIntegrationOptionsV2,
} from "@/api/v2/adminRequests";
import { v2 } from "@/api/v2/request";
import {
  browseDiscoverV2,
  createMediaRequestV2,
  getDiscoverSectionV2,
  getRequestMediaDetailV2,
  listDiscoverGenresV2,
  listDiscoverNetworksV2,
  listDiscoverSectionsV2,
  listDiscoverStudiosV2,
  listMyMediaRequestsV2,
  searchRequestMediaV2,
} from "@/api/v2/requests";
import { useCurrentProfile } from "@/hooks/useCurrentProfile";
import type {
  CreateMediaRequestInput,
  DiscoverBrowseKind,
  LoadRequestIntegrationOptionsRequest,
  RequestIntegration,
  RequestListParams,
  RequestSearchMediaType,
  RequestMediaType,
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

function isValidationFailure(err: unknown): boolean {
  return err instanceof V2ProblemError && err.problemType === "validation_failed";
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
    queryFn: listDiscoverSectionsV2,
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useRequestFeatureStatus() {
  return useQuery({
    queryKey: requestKeys.status(),
    queryFn: () => v2("GET /api/v2/requests/status"),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useRequestDiscoverySection(section: string, page = 1) {
  return useQuery({
    queryKey: requestKeys.discoverySection(section, page),
    queryFn: () => getDiscoverSectionV2(section, page),
    enabled: section.trim().length > 0,
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useDiscoverStudios() {
  return useQuery({
    queryKey: requestKeys.discoverStudios(),
    queryFn: listDiscoverStudiosV2,
    staleTime: DISCOVER_BRAND_STALE_TIME,
  });
}

export function useDiscoverNetworks() {
  return useQuery({
    queryKey: requestKeys.discoverNetworks(),
    queryFn: listDiscoverNetworksV2,
    staleTime: DISCOVER_BRAND_STALE_TIME,
  });
}

export function useDiscoverGenres() {
  return useQuery({
    queryKey: requestKeys.discoverGenres(),
    queryFn: listDiscoverGenresV2,
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
    queryFn: () => browseDiscoverV2({ kind, slug, mediaType, sort, page }),
    enabled: slug.trim().length > 0 && (kind !== "genre" || Boolean(mediaType)),
    staleTime: BROWSE_STALE_TIME,
  });
}

export function useRequestMediaDetail(mediaType: RequestMediaType, tmdbID: number) {
  return useQuery({
    queryKey: requestKeys.detail(mediaType, tmdbID),
    queryFn: () => getRequestMediaDetailV2(mediaType, tmdbID),
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
  /** Inactive cache lifetime for rapidly changing interactive search keys. */
  gcTime?: number;
  /** Retry policy; interactive search surfaces should not replay expensive failures. */
  retry?: boolean | number;
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
    queryFn: ({ signal }) => searchRequestMediaV2(mediaType, normalizedQuery, page, signal),
    enabled:
      enabledOverride && normalizedQuery.length > 1 && (!requireProfile || Boolean(profile?.id)),
    staleTime: options.staleTime ?? REQUESTS_STALE_TIME,
    ...(options.gcTime !== undefined ? { gcTime: options.gcTime } : {}),
    ...(options.retry !== undefined ? { retry: options.retry } : {}),
  });
}

export function useCreateMediaRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (body: CreateMediaRequestInput) => createMediaRequestV2(body),
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
    queryFn: () => listMyMediaRequestsV2(params),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useAdminMediaRequests(params: RequestListParams = {}) {
  const key = listParamsKey(params);
  return useQuery({
    queryKey: adminKeys.requests(key),
    queryFn: () => listAdminMediaRequestsV2(params),
    staleTime: 10_000,
  });
}

export function useApproveMediaRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (id: string) => approveAdminRequestV2(id),
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
    retry: false,
    mutationFn: ({ id, reason }: { id: string; reason?: string }) =>
      declineAdminRequestV2(id, reason),
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
    retry: false,
    mutationFn: (id: string) => retryAdminRequestV2(id),
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
    queryFn: getAdminRequestSettingsV2,
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useUpdateRequestSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: putAdminRequestSettingsV2,
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
    queryFn: listAdminRequestIntegrationsV2,
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useCreateRequestIntegration() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (integration: RequestIntegration) =>
      saveAdminRequestIntegrationV2(integration, true),
    onSuccess: () => {
      toast.success("Integration created");
      queryClient.invalidateQueries({ queryKey: adminKeys.requestIntegrations() });
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      if (isValidationFailure(err)) return;
      toast.error(err instanceof Error ? err.message : "Failed to create integration");
    },
  });
}

export function useUpdateRequestIntegration() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (integration: RequestIntegration) => saveAdminRequestIntegrationV2(integration),
    onSuccess: () => {
      toast.success("Integration saved");
      queryClient.invalidateQueries({ queryKey: adminKeys.requestIntegrations() });
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      if (isValidationFailure(err)) return;
      toast.error(err instanceof Error ? err.message : "Failed to save integration");
    },
  });
}

export function useDeleteRequestIntegration() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: deleteAdminRequestIntegrationV2,
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
    retry: false,
    mutationFn: ({ id, body }: { id: string; body: LoadRequestIntegrationOptionsRequest }) =>
      loadAdminRequestIntegrationOptionsV2(id, body),
    // Silent background probe: callers surface load failures inline (no toast).
  });
}

export function useRequestUserLimit(userId?: number) {
  return useQuery({
    queryKey: adminKeys.requestUserLimit(userId ?? 0),
    queryFn: () => getAdminRequestUserLimitV2(userId!),
    enabled: Boolean(userId && userId > 0),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useUpdateRequestUserLimit() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ userId, body }: { userId: number; body: RequestUserLimit }) =>
      putAdminRequestUserLimitV2(userId, body),
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

export function useAdminRequestCapabilities() {
  return useQuery({
    queryKey: [...adminKeys.requestsRoot(), "capabilities"],
    queryFn: () => v2("GET /api/v2/admin/requests/capabilities"),
    staleTime: REQUESTS_STALE_TIME,
  });
}
