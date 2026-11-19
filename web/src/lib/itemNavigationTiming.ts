import { recentIntentPrefetch } from "@/lib/intentPrefetch";

/**
 * Times item-detail navigations from the click to the first render of the
 * full page, as `silo:item-navigation` User Timing measures. They show in the
 * browser's performance panel and are what intent-prefetch tuning reads: each
 * carries where the click came from, whether the detail was already cached,
 * and which prefetch (if any) warmed it and how far ahead of the click.
 */

/** A start older than this belongs to a navigation that never arrived. */
const PENDING_LIMIT_MS = 10_000;
/** Measures are cleared past this count so a long session does not keep them all. */
const MEASURE_LIMIT = 200;
let measures = 0;

let pending: { contentId: string; startedAt: number; from: string; cached: boolean } | null = null;

export function markItemNavigationStart(contentId: string, cached: boolean) {
  pending = { contentId, startedAt: performance.now(), from: location.pathname, cached };
}

export function markItemDetailShown(contentId: string) {
  const start = pending;
  if (!start || start.contentId !== contentId) return;
  pending = null;
  const end = performance.now();
  if (end - start.startedAt > PENDING_LIMIT_MS || typeof performance.measure !== "function") return;

  const prefetch = recentIntentPrefetch(contentId);
  try {
    if (++measures > MEASURE_LIMIT) {
      performance.clearMeasures("silo:item-navigation");
      measures = 1;
    }
    performance.measure("silo:item-navigation", {
      start: start.startedAt,
      end,
      detail: {
        contentId,
        from: start.from,
        cachedAtClick: start.cached,
        prefetchTrigger: prefetch?.trigger ?? null,
        prefetchLeadMs: prefetch ? Math.round(start.startedAt - prefetch.at) : null,
      },
    });
  } catch {
    // An engine without User Timing options loses the measure, nothing else.
  }
}
