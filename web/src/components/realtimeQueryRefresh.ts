import type { QueryClient, QueryFilters, QueryKey } from "@tanstack/react-query";

const REFRESH_WINDOW_MS = 5_000;

// Keep HTTP authoritative and combine event bursts. Only incoming events or a
// read that predates an event schedule work; this is not an idle poll.
export function createRealtimeQueryRefreshScheduler(
  queryClient: QueryClient,
  authorityActive: () => boolean,
  allowRefetch: (queryKey: QueryKey) => boolean,
) {
  const pending = new Map<string, QueryKey>();
  let timer: number | undefined;
  let refreshing = false;
  let cancelled = false;

  const flush = () => {
    if (cancelled || !authorityActive() || pending.size === 0) return;
    const queries = [...pending];
    pending.clear();
    refreshing = true;
    const settle = () => {
      refreshing = false;
      if (cancelled) return;
      timer = window.setTimeout(() => {
        timer = undefined;
        flush();
      }, REFRESH_WINDOW_MS);
    };
    void Promise.all(
      queries.map(([hash, queryKey]) => {
        const refetchType = allowRefetch(queryKey) ? "active" : "none";
        // Preserve an existing read, then catch up once after it settles.
        if (
          refetchType === "active" &&
          queryClient.getQueryState(queryKey)?.fetchStatus === "fetching"
        ) {
          pending.set(hash, queryKey);
        }
        return queryClient.invalidateQueries(
          { queryKey, exact: true, refetchType },
          { cancelRefetch: false },
        );
      }),
    ).then(settle, settle);
  };

  return {
    schedule(...filters: QueryFilters[]) {
      if (cancelled || !authorityActive()) return;
      for (const filter of filters) {
        for (const query of queryClient.getQueryCache().findAll(filter)) {
          pending.set(query.queryHash, query.queryKey);
        }
      }
      if (!refreshing && timer === undefined) flush();
    },
    cancel() {
      cancelled = true;
      pending.clear();
      if (timer !== undefined) window.clearTimeout(timer);
    },
  };
}
