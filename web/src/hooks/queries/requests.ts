import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type {
  CreateMediaRequestInput,
  LoadRequestIntegrationOptionsRequest,
  MediaRequest,
  MediaRequestsListResponse,
  RequestDiscoveryResponse,
  RequestDiscoverySection,
  RequestIntegration,
  RequestIntegrationOptions,
  RequestIntegrationsResponse,
  RequestListParams,
  RequestMediaDetail,
  RequestMediaPage,
  RequestMediaType,
  RequestSettings,
  RequestUserLimit,
} from "@/api/types";
import { adminKeys, requestKeys } from "./keys";

const REQUESTS_STALE_TIME = 30_000;

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
  if (params.limit) query.set("limit", String(params.limit));
  if (params.offset) query.set("offset", String(params.offset));
  const encoded = query.toString();
  return encoded ? `?${encoded}` : "";
}

function invalidateRequestSurfaces(queryClient: ReturnType<typeof useQueryClient>) {
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

export function useRequestSearch(mediaType: RequestMediaType, query: string, page = 1) {
  const normalizedQuery = query.trim();
  return useQuery({
    queryKey: requestKeys.search(mediaType, normalizedQuery, page),
    queryFn: () => {
      const params = new URLSearchParams({
        q: normalizedQuery,
        media_type: mediaType,
        page: String(page),
      });
      return api<RequestMediaPage>(`/requests/search?${params}`);
    },
    enabled: normalizedQuery.length > 1,
    staleTime: REQUESTS_STALE_TIME,
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

export function useUpdateRequestIntegrations() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (integrations: RequestIntegration[]) =>
      api<RequestIntegrationsResponse>("/admin/request-integrations", {
        method: "PUT",
        body: JSON.stringify({ integrations }),
      }),
    onSuccess: () => {
      toast.success("Integrations saved");
      queryClient.invalidateQueries({ queryKey: adminKeys.requestIntegrations() });
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save integrations");
    },
  });
}

export function useLoadRequestIntegrationOptions() {
  return useMutation({
    mutationFn: ({ kind, body }: { kind: string; body: LoadRequestIntegrationOptionsRequest }) =>
      api<RequestIntegrationOptions>(
        `/admin/request-integrations/${encodeURIComponent(kind)}/options`,
        {
          method: "POST",
          body: JSON.stringify(body),
        },
      ),
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
