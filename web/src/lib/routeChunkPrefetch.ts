/**
 * Idle-time warm-up for the hottest lazy route chunks.
 *
 * Code-splitting the routes keeps the bootstrap bundle small, but it also puts
 * a network round trip in front of the first navigation to each route: the old
 * page stays frozen (or falls back to the app-level Suspense boundary) while
 * the chunk downloads. Warming the handful of routes practically every session
 * reaches keeps the split and removes that stall.
 *
 * Chunks are fetched one at a time, each on its own idle callback, so the
 * warm-up never competes with the first screen's data requests.
 */
export type RouteChunkImport = () => Promise<unknown>;

/** Upper bound on how long a warm-up may sit waiting for an idle window. */
export const ROUTE_CHUNK_IDLE_TIMEOUT_MS = 2_000;

/** Schedules `task` and returns a canceller for it. */
export type RouteChunkScheduler = (task: () => void) => () => void;

/**
 * Fetches each import in order, one per idle window. Returns a canceller that
 * stops the remaining warm-ups; imports already in flight are left alone since
 * their result lands in the module cache either way.
 */
export function prefetchRouteChunks(
  imports: readonly RouteChunkImport[],
  schedule: RouteChunkScheduler = scheduleWhenIdle,
): () => void {
  let cancelled = false;
  let cancelPending: (() => void) | null = null;

  const warmFrom = (index: number) => {
    const load = imports[index];
    if (cancelled || !load) return;
    cancelPending = schedule(() => {
      cancelPending = null;
      if (cancelled) return;
      void load()
        .catch(() => {
          // A failed warm-up is not an error worth reporting: the route retries
          // the import on navigation, where React.lazy surfaces the failure to
          // the error boundary with the user actually waiting on it.
        })
        .finally(() => warmFrom(index + 1));
    });
  };

  warmFrom(0);

  return () => {
    cancelled = true;
    cancelPending?.();
    cancelPending = null;
  };
}

function scheduleWhenIdle(task: () => void): () => void {
  if (typeof globalThis.requestIdleCallback === "function") {
    const handle = globalThis.requestIdleCallback(() => task(), {
      timeout: ROUTE_CHUNK_IDLE_TIMEOUT_MS,
    });
    return () => globalThis.cancelIdleCallback?.(handle);
  }
  const handle = globalThis.setTimeout(task, ROUTE_CHUNK_IDLE_TIMEOUT_MS);
  return () => globalThis.clearTimeout(handle);
}
