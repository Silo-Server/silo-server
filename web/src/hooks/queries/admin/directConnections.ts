import {
  replaceEqualDeep,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";
import { usePageActivity } from "@/hooks/usePageActivity";

export type DirectConnectionsHostStatus = components["schemas"]["DirectConnectionsHostStatus"];
type DirectConnectionsStatus = components["schemas"]["DirectConnectionsStatus"];

const STATUS_POLL_INTERVAL = 15_000;

function statusKey(context: ProfileRequestContextSnapshot | null) {
  return [
    "admin",
    "directConnections",
    "status",
    context?.serverOrigin,
    context?.authContextVersion,
    context?.profileId,
    context?.profileTokenGeneration,
  ] as const;
}

/**
 * Counts, per client and query key, the refresh results published to the cache. Each poll
 * result is marked with the count at its start; the cache compares that mark with the
 * count when the result is written, so a refresh published in between makes the poll
 * answer older than the cache. Request order decides, so no server clock is compared and
 * clock skew between replicas cannot hold an old result.
 */
const refreshPublications = new WeakMap<QueryClient, Map<string, number>>();

function publicationCount(client: QueryClient, queryKey: readonly unknown[]) {
  return refreshPublications.get(client)?.get(JSON.stringify(queryKey)) ?? 0;
}

/** Maps each poll result to the publication count at the start of its request. */
const pollStarts = new WeakMap<object, number>();

function recordPublication(client: QueryClient, queryKey: readonly unknown[]) {
  const counts = refreshPublications.get(client) ?? new Map<string, number>();
  counts.set(JSON.stringify(queryKey), publicationCount(client, queryKey) + 1);
  refreshPublications.set(client, counts);
}

/** Reads the last completed check while visible; polling starts no new probes. */
export function useAdminDirectConnectionsStatus() {
  const pageActivity = usePageActivity();
  const queryClient = useQueryClient();
  const profileContext = captureProfileRequestContext();
  const queryKey = statusKey(profileContext);
  return useQuery({
    queryKey,
    enabled: profileContext !== null,
    queryFn: async ({ signal }) => {
      if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const published = publicationCount(queryClient, queryKey);
      const result = await v2("GET /api/v2/admin/direct-connections/status", {
        profileContext,
        signal,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      pollStarts.set(result, published);
      return result;
    },
    // Runs when React Query writes a fetch or setQueryData result to the cache. The decision
    // sits here, not in queryFn, so a refresh published after queryFn returns still wins.
    // A result with no mark (a refresh result) always publishes.
    structuralSharing: (cached, incoming) => {
      const started = pollStarts.get(incoming as object);
      if (
        cached !== undefined &&
        started !== undefined &&
        publicationCount(queryClient, queryKey) !== started
      )
        return cached;
      return replaceEqualDeep(cached, incoming);
    },
    staleTime: STATUS_POLL_INTERVAL,
    refetchInterval: pageActivity.canApplyRealtimeUpdates ? STATUS_POLL_INTERVAL : false,
  });
}

/** Runs one check without replay, publishing only to the initiating authority. */
export function useRefreshDirectConnections() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (profileContext: ProfileRequestContextSnapshot | null) => {
      if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const queryKey = statusKey(profileContext);
      await queryClient.cancelQueries({ queryKey, exact: true });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("GET /api/v2/admin/direct-connections/status", {
        query: { refresh: true },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return result;
    },
    onSuccess: (result, profileContext) => {
      if (!profileContext || !isCapturedProfileAuthorityActive(profileContext)) return;
      const queryKey = statusKey(profileContext);
      queryClient.setQueryData<DirectConnectionsStatus>(queryKey, result);
      recordPublication(queryClient, queryKey);
    },
  });
  return {
    ...mutation,
    mutate: () => mutation.mutate(captureProfileRequestContext()),
  };
}
