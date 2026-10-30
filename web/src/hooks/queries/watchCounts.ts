import { useQuery, type QueryClient } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";
import { watchCountKeys } from "@/hooks/queries/keys";

/**
 * A series' unwatched episode count, for the unwatched-episodes card badge.
 * Cards carry only the played flag (exact counts cost the server far more), so
 * the badge asks queryCatalogWatchCounts for the series it draws. Every card
 * that asks in the same tick joins one request, and each series caches on its
 * own, so a row of cards costs one request and a later row reuses what it can.
 */

/** The server's bound on series per request (CatalogWatchCountsQuery). */
export const MAX_WATCH_COUNT_SERIES = 250;

interface Waiter {
  resolve: (count: number | null) => void;
  reject: (error: unknown) => void;
}

let queued = new Map<string, Waiter[]>();
let flushScheduled = false;

function flushQueued() {
  flushScheduled = false;
  const batch = queued;
  queued = new Map();
  const seriesIds = [...batch.keys()];
  for (let start = 0; start < seriesIds.length; start += MAX_WATCH_COUNT_SERIES) {
    const chunk = seriesIds.slice(start, start + MAX_WATCH_COUNT_SERIES);
    const waiters = (id: string) => batch.get(id) ?? [];
    v2("POST /api/v2/catalog/watch-counts", { body: { series_ids: chunk } }).then(
      (page) => {
        const counts = new Map(
          page.items.map((item) => [item.series_id, item.user_data.unplayed_count]),
        );
        // A series the server left out (hidden, or no available episodes)
        // has no count, which is not the same as nothing left to watch.
        for (const id of chunk) {
          for (const waiter of waiters(id)) waiter.resolve(counts.get(id) ?? null);
        }
      },
      (error: unknown) => {
        for (const id of chunk) {
          for (const waiter of waiters(id)) waiter.reject(error);
        }
      },
    );
  }
}

function loadUnwatchedEpisodeCount(seriesId: string): Promise<number | null> {
  return new Promise((resolve, reject) => {
    const waiters = queued.get(seriesId);
    if (waiters) waiters.push({ resolve, reject });
    else queued.set(seriesId, [{ resolve, reject }]);
    if (!flushScheduled) {
      flushScheduled = true;
      setTimeout(flushQueued, 0);
    }
  });
}

/**
 * The series' unwatched episode count: undefined while it loads, null when the
 * server has none for it. In-progress episodes count as unwatched until they
 * are finished, as on the series page.
 */
export function useUnwatchedEpisodeCount(seriesId: string): number | null | undefined {
  const { data } = useQuery({
    queryKey: watchCountKeys.series(seriesId),
    queryFn: () => loadUnwatchedEpisodeCount(seriesId),
  });
  return data;
}

// Progress ticks arrive every few seconds while something plays, so they
// refetch the counts on screen at most once per window, like Home's refresh.
const PROGRESS_REFRESH_WINDOW_MS = 30_000;
const progressRefreshTimers = new Map<string, ReturnType<typeof setTimeout>>();

/**
 * Refreshes a series' count after the profile's watch state changed. A watched
 * mark or a history change refetches it at once. Progress only marks it stale,
 * so a card mounted later (on leaving the player, say) reads it again, and
 * refetches the cards on screen once per window.
 */
export function refreshUnwatchedEpisodeCount(
  queryClient: QueryClient,
  seriesId: string,
  change: string,
) {
  const queryKey = watchCountKeys.series(seriesId);
  // Nothing to refresh for a series no card has counted (or a movie's ID).
  if (!queryClient.getQueryCache().find({ queryKey, exact: true })) return;
  if (change !== "progress") {
    void queryClient.invalidateQueries({ queryKey });
    return;
  }
  void queryClient.invalidateQueries({ queryKey, refetchType: "none" });
  if (progressRefreshTimers.has(seriesId)) return;
  progressRefreshTimers.set(
    seriesId,
    setTimeout(() => {
      progressRefreshTimers.delete(seriesId);
      void queryClient.invalidateQueries({ queryKey });
    }, PROGRESS_REFRESH_WINDOW_MS),
  );
}
