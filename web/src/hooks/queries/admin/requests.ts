/**
 * Request administration: the queue, request settings, servers, routing and
 * limits. Kept apart from the requester hooks so the launch bundle, which
 * loads those for search and title pages, carries none of this.
 */
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";
import { problemFieldDetail, V2ProblemError } from "@/api/v2/request";
import { t } from "@/i18n";
import {
  getAdminRequestSettingsV2,
  putAdminRequestSettingsV2,
  listAdminRequestIntegrationsV2,
  saveAdminRequestIntegrationV2,
  deleteAdminRequestIntegrationV2,
  listAdminRequestQueuePageV2,
  getAdminRequestCountsV2,
  listAdminRequestEventsV2,
  approveAdminRequestV2,
  cancelAdminRequestV2,
  declineAdminRequestV2,
  retryAdminRequestV2,
  loadAdminRequestIntegrationOptionsV2,
  listAdminRequestRoutesV2,
  createAdminRequestRouteV2,
  updateAdminRequestRouteV2,
  deleteAdminRequestRouteV2,
  reorderAdminRequestRoutesV2,
  previewAdminRequestRouteV2,
  searchAdminRequestRouteTitlesV2,
  getAdminRequestRoutingV2,
  putAdminRequestRoutingV2,
  type AdminRequestQueueFilter,
  type RequestRoute,
  type RequestRouteBody,
  type RequestRouteMediaType,
  type RequestRouting,
  type RequestRoutingMode,
} from "@/api/v2/adminRequests";
import { v2 } from "@/api/v2/request";
import type {
  LoadRequestIntegrationOptionsRequest,
  MediaRequest,
  RequestIntegration,
} from "@/api/types";
import { adminKeys, requestKeys } from "../keys";

import {
  REQUESTS_STALE_TIME,
  invalidateRequestSurfaces,
  isValidationFailure,
  requestDownloadRefetchInterval,
} from "../useRequests";

export {
  useRequestGroupLimit,
  useRequestUserLimit,
  useUpdateRequestGroupLimit,
  useUpdateRequestUserLimit,
} from "./requestLimits";

const ADMIN_QUEUE_STALE_TIME = 10_000;
/** Rows per queue page; the server answers at most 50. */
export const ADMIN_QUEUE_PAGE_SIZE = 25;
/** How often the view counts (and so the admin nav's badge) are read again. */
export const ADMIN_REQUEST_COUNTS_INTERVAL = 60_000;

function requestQueueKey(filter: AdminRequestQueueFilter) {
  return adminKeys.requestQueue({
    view: filter.view,
    q: filter.q?.trim() ?? "",
    mediaType: filter.mediaType ?? "all",
    requestedByUserId: filter.requestedByUserId ?? null,
  });
}

/**
 * One queue view, a page at a time. A new search or type filter keeps the
 * rows on screen until its first page arrives; a new view does not, since its
 * rows take different actions. With `enabled: false` it only reads the rows
 * another reader of the same view loads, and follows their refetches. While
 * a loaded row downloads, the view is read again every 30 seconds.
 */
export function useAdminRequestQueue(
  filter: AdminRequestQueueFilter,
  options: { enabled?: boolean } = {},
) {
  return useInfiniteQuery({
    queryKey: requestQueueKey(filter),
    enabled: options.enabled ?? true,
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam, signal }) =>
      listAdminRequestQueuePageV2(filter, {
        limit: ADMIN_QUEUE_PAGE_SIZE,
        cursor: pageParam,
        signal,
      }),
    // A cursor the list already visited would loop; stop there.
    getNextPageParam: (last, _pages, _lastParam, params) =>
      last.nextCursor && !params.includes(last.nextCursor) ? last.nextCursor : undefined,
    placeholderData: (previous, previousQuery) =>
      (previousQuery?.queryKey[3] as { view?: string } | undefined)?.view === filter.view
        ? previous
        : undefined,
    staleTime: ADMIN_QUEUE_STALE_TIME,
    refetchInterval: (query) =>
      requestDownloadRefetchInterval(query.state.data?.pages.flatMap((page) => page.items)),
  });
}

/** How many requests each queue view holds; polled for the admin nav badge. */
export function useAdminRequestCounts(options: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: adminKeys.requestCounts(),
    queryFn: getAdminRequestCountsV2,
    enabled: options.enabled ?? true,
    staleTime: ADMIN_QUEUE_STALE_TIME,
    // A server without request administration, or an admin session that lost
    // its rights, answers the same every time; any other error (a node
    // restarting) is worth asking again.
    refetchInterval: (query) =>
      query.state.error instanceof V2ProblemError &&
      ["permission_denied", "dependency_unavailable"].includes(query.state.error.problemType)
        ? false
        : ADMIN_REQUEST_COUNTS_INTERVAL,
    retry: false,
  });
}

/** Reads the queue's rows and view counts again. */
export function useRefreshRequestQueue() {
  const queryClient = useQueryClient();
  const [isRefreshing, setRefreshing] = useState(false);
  return {
    isRefreshing,
    refresh: () => {
      setRefreshing(true);
      void Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.requestQueueRoot() }),
        queryClient.invalidateQueries({ queryKey: adminKeys.requestCounts() }),
      ]).finally(() => setRefreshing(false));
    },
  };
}

/** A request's history, newest first. */
export function useAdminRequestEvents(id: string | undefined) {
  return useQuery({
    queryKey: adminKeys.requestEvents(id ?? ""),
    queryFn: () => listAdminRequestEventsV2(id!),
    enabled: Boolean(id),
    staleTime: ADMIN_QUEUE_STALE_TIME,
  });
}

/** Where a request's quality tiers would go if it were sent now. */
export function useAdminRequestRoutePreview(
  target: { mediaType: RequestRouteMediaType; tmdbId: number; requesterUserId?: number } | null,
) {
  return useQuery({
    queryKey: adminKeys.requestRoutePreview(
      target?.mediaType ?? "",
      target?.tmdbId ?? 0,
      target?.requesterUserId,
    ),
    queryFn: () =>
      previewAdminRequestRouteV2(target!.mediaType, target!.tmdbId, target!.requesterUserId),
    enabled: target !== null,
    staleTime: REQUESTS_STALE_TIME,
    retry: false,
  });
}

export function useApproveMediaRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, options }: { id: string; options?: Record<string, string> }) =>
      approveAdminRequestV2(id, options),
    onSuccess: () => {
      toast.success(t("web.admin.requests.toast.approved"));
    },
    // A refused action still refreshes the queue: another admin may have
    // acted first, and the row should show what happened.
    onSettled: () => invalidateRequestSurfaces(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : t("web.admin.requests.toast.approveFailed"));
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
      toast.success(t("web.admin.requests.toast.declined"));
    },
    // A refused action still refreshes the queue: another admin may have
    // acted first, and the row should show what happened.
    onSettled: () => invalidateRequestSurfaces(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : t("web.admin.requests.toast.declineFailed"));
    },
  });
}

export function useRetryMediaRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (id: string) => retryAdminRequestV2(id),
    onSuccess: () => {
      toast.success(t("web.admin.requests.toast.retried"));
    },
    // A refused action still refreshes the queue: another admin may have
    // acted first, and the row should show what happened.
    onSettled: () => invalidateRequestSurfaces(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : t("web.admin.requests.toast.retryFailed"));
    },
  });
}

/** An admin withdraws a request nothing has been sent for yet, or closes a failed one. */
export function useAdminCancelMediaRequest() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, reason }: { id: string; reason?: string }) =>
      cancelAdminRequestV2(id, reason),
    onSuccess: () => {
      toast.success(t("web.admin.requests.toast.cancelled"));
    },
    // A refused action still refreshes the queue: another admin may have
    // acted first, and the row should show what happened.
    onSettled: () => invalidateRequestSurfaces(queryClient),
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : t("web.admin.requests.toast.cancelFailed"));
    },
  });
}

export type BulkRequestAction = "approve" | "decline";

/** Requests a bulk action sends at once; the rest wait for a free slot. */
export const BULK_REQUEST_CONCURRENCY = 4;

export interface BulkRequestFailure {
  id: string;
  title: string;
  message: string;
}

export interface BulkRequestProgress {
  action: BulkRequestAction;
  total: number;
  /** Requests answered so far, failed ones included. */
  done: number;
  failures: BulkRequestFailure[];
}

async function forEachWithConcurrency<T>(
  items: readonly T[],
  limit: number,
  run: (item: T) => Promise<void>,
) {
  let next = 0;
  const worker = async () => {
    while (next < items.length) await run(items[next++]!);
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
}

/**
 * Approves or declines several requests through the per-request endpoints, a
 * few at a time. One refusal does not stop the rest: `progress` counts every
 * answer and keeps each failure with its reason. The queue and counts are
 * read again once, when the last answer is in.
 */
export function useBulkRequestAction() {
  const queryClient = useQueryClient();
  const [progress, setProgress] = useState<BulkRequestProgress | null>(null);
  const mutation = useMutation({
    retry: false,
    mutationFn: async ({
      action,
      requests,
      reason,
    }: {
      action: BulkRequestAction;
      requests: readonly Pick<MediaRequest, "id" | "title">[];
      reason?: string;
    }) => {
      const failures: BulkRequestFailure[] = [];
      let done = 0;
      setProgress({ action, total: requests.length, done, failures: [] });
      await forEachWithConcurrency(requests, BULK_REQUEST_CONCURRENCY, async (request) => {
        try {
          if (action === "approve") await approveAdminRequestV2(request.id);
          else await declineAdminRequestV2(request.id, reason);
        } catch (err) {
          failures.push({
            id: request.id,
            title: request.title,
            message: problemMessage(
              err,
              action === "approve"
                ? t("web.admin.requests.toast.approveFailed")
                : t("web.admin.requests.toast.declineFailed"),
            ),
          });
        }
        done += 1;
        setProgress({ action, total: requests.length, done, failures: [...failures] });
      });
      return { action, total: requests.length, failures };
    },
    onSuccess: ({ action, total, failures }) => {
      const approved = action === "approve";
      const succeeded = total - failures.length;
      if (failures.length === 0) {
        const values = { count: succeeded, count_text: succeeded };
        toast.success(
          approved
            ? t("web.admin.requests.toast.bulkApproved", values)
            : t("web.admin.requests.toast.bulkDeclined", values),
        );
      } else {
        const values = {
          count: total,
          count_text: total,
          failed: failures.length,
        };
        toast.error(
          approved
            ? t("web.admin.requests.toast.bulkApproveFailed", values)
            : t("web.admin.requests.toast.bulkDeclineFailed", values),
        );
      }
    },
    onSettled: () => invalidateRequestSurfaces(queryClient),
  });
  return {
    run: mutation.mutate,
    isRunning: mutation.isPending,
    progress,
    /** Forgets the last run's progress and failures. */
    reset: () => {
      mutation.reset();
      setProgress(null);
    },
  };
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
    onSuccess: (saved) => {
      toast.success(t("web.admin.requests.toast.settingsSaved"));
      // The editor adopts the saved record; the cache has to hold it first,
      // or a clean editor would follow the query back to the replaced one.
      queryClient.setQueryData(adminKeys.requestSettings(), saved);
      queryClient.invalidateQueries({ queryKey: requestKeys.status() });
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.requests.toast.settingsSaveFailed"),
      );
      // A refused save (412) means someone else saved; read their version so
      // a discarded draft starts from it.
      queryClient.invalidateQueries({ queryKey: adminKeys.requestSettings() });
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

// A saved server's connection may have changed, so the root folders, quality
// profiles, and tags read from it are stale too. Adding or deleting a server
// can also set or clear a media type's Everything else, so the routes are read
// again as well.
function invalidateRequestServers(queryClient: ReturnType<typeof useQueryClient>) {
  queryClient.invalidateQueries({ queryKey: adminKeys.requestIntegrations() });
  queryClient.invalidateQueries({ queryKey: adminKeys.requestIntegrationOptionsRoot() });
  invalidateRequestSurfaces(queryClient);
  void invalidateRequestRoutes(queryClient);
  // A server can turn Advanced on, and changes where Standard sends requests.
  void queryClient.invalidateQueries({ queryKey: adminKeys.requestRouting() });
}

/** "radarr" or "sonarr" for the toast's select (movies, series), "other" for anything else. */
function serverKind(saved: RequestIntegration): string {
  const kind = saved.plugin_config?.service_kind;
  return kind === "radarr" || kind === "sonarr" ? kind : "other";
}

/**
 * The toast for a saved server. Saving one can turn Advanced routing on (a
 * second server of a kind), and the first server of a kind starts taking its
 * media type's requests. Rather than guess the server's rules, the routing is
 * read again and the toast says what changed.
 */
async function savedServerMessage(
  queryClient: ReturnType<typeof useQueryClient>,
  saved: RequestIntegration,
  before: RequestRouting | undefined,
  added: boolean,
): Promise<string> {
  const plain = added
    ? t("web.admin.requests.toast.serverAdded")
    : t("web.admin.requests.toast.serverSaved");
  try {
    const routing = await queryClient.fetchQuery({
      queryKey: adminKeys.requestRouting(),
      queryFn: getAdminRequestRoutingV2,
      staleTime: 0,
    });
    if (before?.mode === "standard" && routing.mode === "advanced") {
      const values = { name: saved.name, kind: serverKind(saved) };
      return added
        ? t("web.admin.requests.toast.serverAddedAdvanced", values)
        : t("web.admin.requests.toast.serverSavedAdvanced", values);
    }
    if (!added) return plain;
    if (routing.mode === "standard") {
      const destination = routing.standard.find(
        (d) => d.hd_integration_id === saved.id || d.uhd_integration_id === saved.id,
      );
      if (destination?.uhd_integration_id === saved.id) {
        return t("web.admin.requests.toast.serverAddedUhd", {
          name: saved.name,
          media_type: destination.media_type,
        });
      }
      if (destination) {
        return t("web.admin.requests.toast.serverAddedEvery", {
          name: saved.name,
          media_type: destination.media_type,
        });
      }
      return plain;
    }
    const routes = await queryClient.fetchQuery({
      queryKey: adminKeys.requestRoutes(),
      queryFn: listAdminRequestRoutesV2,
      staleTime: 0,
    });
    const fallback = routes.find(
      (route) => route.is_fallback && route.hd.integration_id === saved.id,
    );
    if (fallback) {
      return t("web.admin.requests.toast.serverAddedEvery", {
        name: saved.name,
        media_type: fallback.media_type,
      });
    }
  } catch {
    // The routing could not be read; the plain toast is still true.
  }
  return plain;
}

function useSaveRequestIntegration(added: boolean) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (integration: RequestIntegration) =>
      saveAdminRequestIntegrationV2(integration, added),
    // The routing before the save, to tell whether the save turned Advanced on.
    onMutate: () => queryClient.getQueryData<RequestRouting>(adminKeys.requestRouting()),
    onSuccess: (saved, _integration, before) => {
      invalidateRequestServers(queryClient);
      void savedServerMessage(queryClient, saved, before, added).then((message) =>
        toast.success(message),
      );
    },
    onError: (err) => {
      if (isValidationFailure(err)) return;
      toast.error(
        err instanceof Error
          ? err.message
          : added
            ? t("web.admin.requests.toast.serverAddFailed")
            : t("web.admin.requests.toast.serverSaveFailed"),
      );
    },
  });
}

export function useCreateRequestIntegration() {
  return useSaveRequestIntegration(true);
}

export function useUpdateRequestIntegration() {
  return useSaveRequestIntegration(false);
}

export function useDeleteRequestIntegration() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: deleteAdminRequestIntegrationV2,
    onSuccess: () => {
      toast.success(t("web.admin.requests.toast.serverDeleted"));
      invalidateRequestServers(queryClient);
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.requests.toast.serverDeleteFailed"),
      );
    },
  });
}

/**
 * The root folders, quality profiles, and tags a saved server offers, read
 * through the server's own stored connection. A routing destination picks its
 * overrides from these.
 */
export function useRequestIntegrationOptions(integrationId: string | undefined) {
  return useQuery({
    queryKey: adminKeys.requestIntegrationOptions(integrationId ?? ""),
    queryFn: () => loadAdminRequestIntegrationOptionsV2(integrationId!, { base_url: "" }),
    enabled: Boolean(integrationId),
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
}

export function useRequestRouting(enabled = true) {
  return useQuery({
    queryKey: adminKeys.requestRouting(),
    queryFn: getAdminRequestRoutingV2,
    staleTime: REQUESTS_STALE_TIME,
    enabled,
  });
}

/** Switches between Standard and Advanced routing; it saves right away. */
export function useUpdateRequestRouting() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ mode, current }: { mode: RequestRoutingMode; current: RequestRouting }) =>
      putAdminRequestRoutingV2(mode, current),
    onSuccess: (saved) => {
      queryClient.setQueryData(adminKeys.requestRouting(), saved);
      toast.success(
        saved.mode === "standard"
          ? t("web.admin.requests.toast.standardRoutingOn")
          : t("web.admin.requests.toast.advancedRoutingOn"),
      );
      // Advanced can fill in Everything else; a preview answers differently.
      void invalidateRequestRoutes(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : t("web.admin.requests.toast.routingFailed"));
      void queryClient.invalidateQueries({ queryKey: adminKeys.requestRouting() });
    },
  });
}

export function useRequestRoutes(enabled = true) {
  return useQuery({
    queryKey: adminKeys.requestRoutes(),
    queryFn: listAdminRequestRoutesV2,
    staleTime: REQUESTS_STALE_TIME,
    enabled,
  });
}

// Reordering, like any save, advances the revision of every route it touches,
// so the whole list is read again rather than patched in place. The promise
// is returned so a mutation stays pending until the list is fresh: a second
// reorder or toggle computed from the old list would undo the first or carry
// a replaced validator.
function invalidateRequestRoutes(queryClient: ReturnType<typeof useQueryClient>) {
  // An open preview asks TMDB again, so it refreshes without holding the list up.
  void queryClient.invalidateQueries({ queryKey: adminKeys.requestRoutePreviewRoot() });
  return queryClient.invalidateQueries({ queryKey: adminKeys.requestRoutes(), exact: true });
}

/** Writes a saved route into the list, so an editor adopting it is not pulled back. */
function storeRequestRoute(queryClient: ReturnType<typeof useQueryClient>, saved: RequestRoute) {
  queryClient.setQueryData<RequestRoute[]>(adminKeys.requestRoutes(), (routes) =>
    routes?.map((route) => (route.id === saved.id ? saved : route)),
  );
}

/** A problem in one line: its detail and every field error it carries. */
function problemMessage(err: unknown, fallback: string): string {
  if (!(err instanceof Error)) return fallback;
  if (!(err instanceof V2ProblemError)) return err.message;
  const details = [err.message, ...(err.problem.errors ?? []).map(problemFieldDetail)];
  return [...new Set(details.filter(Boolean))].join(" ");
}

/**
 * Toasts a failed route write. A caller that shows validation errors beside
 * its fields passes `inline`; anyone else (a toggle, a reorder) would
 * otherwise fail without a word.
 */
function routeMutationError(err: unknown, fallback: string, inline = false) {
  if (inline && isValidationFailure(err)) return;
  toast.error(problemMessage(err, fallback));
}

/** Adds a rule from the rule editor, which shows validation errors itself. */
export function useCreateRequestRoute() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (body: RequestRouteBody) => createAdminRequestRouteV2(body),
    onSuccess: () => {
      toast.success(t("web.admin.requests.toast.ruleAdded"));
      return invalidateRequestRoutes(queryClient);
    },
    onError: (err) => routeMutationError(err, t("web.admin.requests.toast.ruleAddFailed"), true),
  });
}

/** `inlineErrors`: the caller shows validation errors beside its fields. */
export function useUpdateRequestRoute({ inlineErrors = false }: { inlineErrors?: boolean } = {}) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      route,
      body,
    }: {
      route: Pick<RequestRoute, "id" | "etag">;
      body: RequestRouteBody;
    }) => updateAdminRequestRouteV2(route, body),
    onSuccess: (saved) => {
      toast.success(
        saved.is_fallback
          ? t("web.admin.requests.toast.fallbackSaved")
          : t("web.admin.requests.toast.ruleSaved"),
      );
      storeRequestRoute(queryClient, saved);
    },
    onError: (err) =>
      routeMutationError(err, t("web.admin.requests.toast.routeSaveFailed"), inlineErrors),
    // After a refused save too: the list then carries the other admin's version.
    onSettled: () => invalidateRequestRoutes(queryClient),
  });
}

export function useDeleteRequestRoute() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (route: Pick<RequestRoute, "id" | "etag">) => deleteAdminRequestRouteV2(route),
    onSuccess: () => {
      toast.success(t("web.admin.requests.toast.ruleDeleted"));
    },
    onError: (err) => routeMutationError(err, t("web.admin.requests.toast.ruleDeleteFailed")),
    onSettled: () => invalidateRequestRoutes(queryClient),
  });
}

export function useReorderRequestRoutes() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ mediaType, ids }: { mediaType: RequestRouteMediaType; ids: string[] }) =>
      reorderAdminRequestRoutesV2(mediaType, ids),
    // The new order shows at once, so a dragged row stays where it was
    // dropped. The list stays locked until the reorder is read back (the
    // mutation is pending until then), and a refused reorder puts it back.
    onMutate: async ({ mediaType, ids }) => {
      const key = adminKeys.requestRoutes();
      await queryClient.cancelQueries({ queryKey: key, exact: true });
      const previous = queryClient.getQueryData<RequestRoute[]>(key);
      queryClient.setQueryData<RequestRoute[]>(key, (routes) =>
        routes?.map((route) =>
          route.media_type === mediaType && !route.is_fallback && ids.includes(route.id)
            ? { ...route, position: ids.indexOf(route.id) }
            : route,
        ),
      );
      return { previous };
    },
    onError: (err, _variables, context) => {
      if (context?.previous) {
        queryClient.setQueryData(adminKeys.requestRoutes(), context.previous);
      }
      routeMutationError(err, t("web.admin.requests.toast.reorderFailed"));
    },
    onSettled: () => invalidateRequestRoutes(queryClient),
  });
}

/**
 * Titles to try the routing rules on. Admin-only, and it answers while
 * requests are turned off, unlike the requesters' search.
 */
export function useRequestRouteTitles(
  mediaType: RequestRouteMediaType,
  query: string,
  options: { enabled?: boolean } = {},
) {
  const q = query.trim();
  return useQuery({
    queryKey: adminKeys.requestRouteTitles(mediaType, q),
    queryFn: ({ signal }) => searchAdminRequestRouteTitlesV2(mediaType, q, signal),
    enabled: (options.enabled ?? true) && q.length > 1,
    staleTime: 5 * 60 * 1000,
    retry: false,
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

export function useAdminRequestCapabilities() {
  return useQuery({
    queryKey: [...adminKeys.requestsRoot(), "capabilities"],
    queryFn: () => v2("GET /api/v2/admin/requests/capabilities"),
    staleTime: REQUESTS_STALE_TIME,
  });
}
