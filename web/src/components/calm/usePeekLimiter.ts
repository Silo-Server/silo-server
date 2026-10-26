/**
 * Poster peeks: the first few titles shown at the start of a list row. Each
 * peek is one request, so it loads only once its row is on screen, stays
 * fresh for a few minutes, and shares one small budget of requests in flight
 * with every other peek on the page.
 */
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useIntersectionObserver } from "@/hooks/useIntersectionObserver";

/** Peeks loading at once across the page. */
export const PEEK_MAX_IN_FLIGHT = 4;
export const PEEK_STALE_MS = 5 * 60 * 1000;
export const PEEK_GC_MS = 30 * 60 * 1000;

/** One title in a peek or a preview strip. */
export interface PreviewItem {
  id: string;
  title: string;
  posterUrl?: string;
  thumbhash?: string;
}

/** What a list hands a row's peek: a cache key that names what the row shows, and its fetch. */
export interface PeekRequest {
  queryKey: readonly unknown[];
  fetch(signal: AbortSignal): Promise<PreviewItem[]>;
  /** Titles to show while the peek loads; it still fetches once. */
  placeholder?: () => PreviewItem[] | undefined;
}

/**
 * Runs at most `max` tasks at once; the rest wait in order. A waiting task
 * whose signal aborts leaves the line without ever running.
 */
export function createLimiter(max: number) {
  let running = 0;
  const waiting: Array<() => void> = [];

  function release() {
    // Hand the slot straight to the next task so nothing can slip in between.
    const next = waiting.shift();
    if (next) next();
    else running -= 1;
  }

  function acquire(signal?: AbortSignal): Promise<void> {
    if (signal?.aborted) return Promise.reject(signal.reason);
    if (running < max) {
      running += 1;
      return Promise.resolve();
    }
    return new Promise((resolve, reject) => {
      const start = () => {
        signal?.removeEventListener("abort", leave);
        resolve();
      };
      const leave = () => {
        waiting.splice(waiting.indexOf(start), 1);
        reject(signal?.reason);
      };
      waiting.push(start);
      signal?.addEventListener("abort", leave, { once: true });
    });
  }

  return async function run<T>(task: () => Promise<T>, signal?: AbortSignal): Promise<T> {
    await acquire(signal);
    try {
      return await task();
    } finally {
      release();
    }
  };
}

/** The one budget every peek on the page shares. */
const runPeek = createLimiter(PEEK_MAX_IN_FLIGHT);

const NOTHING_YET: PreviewItem[] = [];

/**
 * A row's peek titles, loaded once the element `observe` is attached to comes
 * near the screen, through the page's shared budget of requests in flight.
 * Empty until they load, and when there are none.
 *
 * The element is observed whether or not the titles are cached, so a
 * remounted row with a stale peek still refreshes once it reaches the screen.
 */
export function usePeekLimiter(request: PeekRequest) {
  const [seen, setSeen] = useState(false);
  const observe = useIntersectionObserver({
    onIntersect: () => setSeen(true),
    enabled: !seen,
    rootMargin: "200px",
  });
  const query = useQuery<PreviewItem[]>({
    queryKey: request.queryKey,
    queryFn: ({ signal }) => runPeek(() => request.fetch(signal), signal),
    placeholderData: () => request.placeholder?.(),
    enabled: seen,
    staleTime: PEEK_STALE_MS,
    gcTime: PEEK_GC_MS,
    retry: false,
    refetchOnWindowFocus: false,
  });
  return { observe, items: query.data ?? NOTHING_YET };
}
