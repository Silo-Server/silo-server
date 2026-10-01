import type { QueryClient } from "@tanstack/react-query";
import { adminKeys, libraryKeys, sectionKeys } from "@/hooks/queries/keys";
import { invalidateMediaSurfaceQueries } from "@/hooks/queries/mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

export interface CatalogInvalidationOptions {
  itemId?: string;
  libraryId?: number;
  allowDashboardRefetch: boolean;
  includeLibraryLists?: boolean;
}

export function invalidateCatalogState(
  queryClient: QueryClient,
  options: CatalogInvalidationOptions,
) {
  const { itemId, libraryId, allowDashboardRefetch, includeLibraryLists = true } = options;
  // Library-scoped sweeps leave home section queries out (see
  // activeSectionQueryMatchesLibrary) so a scan cannot storm them — but the
  // refresh-signal bump below still reloads Home's rows through fetchQuery,
  // which would happily serve the fresh-but-outdated cache. Mark home data
  // stale without refetching, so only the (already throttled) queue reset
  // fetches, and it fetches real data.
  if (libraryId !== undefined) {
    void queryClient.invalidateQueries({ queryKey: sectionKeys.home(), refetchType: "none" });
  }
  void invalidateMediaSurfaceQueries(queryClient, { itemId, libraryId }).then(() => {
    bumpHomeRefreshSignal(queryClient);
  });
  if (includeLibraryLists) {
    void queryClient.invalidateQueries({
      queryKey: adminKeys.libraries(),
      refetchType: allowDashboardRefetch ? "active" : "none",
    });
    void queryClient.invalidateQueries({ queryKey: adminKeys.libraryMatchQueueStatuses() });
    void queryClient.invalidateQueries({ queryKey: libraryKeys.all });
  }
  void queryClient.invalidateQueries({
    queryKey: adminKeys.stats(),
    refetchType: allowDashboardRefetch ? "active" : "none",
  });
}

/**
 * A scan emits one catalog event per file it touches. Running the full
 * invalidation sweep for each of them refetches every open surface hundreds of
 * times a minute for data that is still changing, so events are coalesced into
 * one sweep per window.
 */
export const CATALOG_INVALIDATION_WINDOW_MS = 2_000;

interface PendingCatalogInvalidation {
  itemIds: Set<string>;
  libraryIds: Set<number>;
  hasUnscopedEvent: boolean;
  allowDashboardRefetch: boolean;
  includeLibraryLists: boolean;
}

export interface CatalogInvalidationScheduler {
  /** Invalidates now if the window is open, otherwise folds into its flush. */
  schedule: (options: CatalogInvalidationOptions) => void;
  /** Drops anything still queued. Call on teardown. */
  cancel: () => void;
}

/**
 * Throttles catalog invalidation to one sweep per window, with the first event
 * after an idle period applied immediately so an isolated change (a single
 * metadata edit) still lands without delay.
 *
 * Merging widens rather than narrows: a window that saw two libraries, or any
 * event with no library at all, flushes unscoped, and a window that saw several
 * items drops the per-item keys — the broad `items`/`catalog` invalidations
 * inside `invalidateMediaSurfaceQueries` already cover every item detail.
 */
export function createCatalogInvalidationScheduler(
  queryClient: QueryClient,
  windowMs: number = CATALOG_INVALIDATION_WINDOW_MS,
): CatalogInvalidationScheduler {
  let windowTimer: number | undefined;
  let pending: PendingCatalogInvalidation | null = null;

  const flush = () => {
    const batch = pending;
    pending = null;
    windowTimer = window.setTimeout(onWindowClosed, windowMs);
    if (batch) {
      invalidateCatalogState(queryClient, resolveBatch(batch));
    }
  };

  const onWindowClosed = () => {
    windowTimer = undefined;
    if (pending) flush();
  };

  return {
    schedule(options) {
      pending = mergeCatalogInvalidation(pending, options);
      if (windowTimer === undefined) flush();
    },
    cancel() {
      if (windowTimer !== undefined) window.clearTimeout(windowTimer);
      windowTimer = undefined;
      pending = null;
    },
  };
}

function mergeCatalogInvalidation(
  pending: PendingCatalogInvalidation | null,
  options: CatalogInvalidationOptions,
): PendingCatalogInvalidation {
  const next: PendingCatalogInvalidation = pending ?? {
    itemIds: new Set(),
    libraryIds: new Set(),
    hasUnscopedEvent: false,
    allowDashboardRefetch: false,
    includeLibraryLists: false,
  };

  if (options.itemId) next.itemIds.add(options.itemId);
  if (options.libraryId === undefined) {
    next.hasUnscopedEvent = true;
  } else {
    next.libraryIds.add(options.libraryId);
  }
  next.allowDashboardRefetch = next.allowDashboardRefetch || options.allowDashboardRefetch;
  next.includeLibraryLists = next.includeLibraryLists || (options.includeLibraryLists ?? true);

  return next;
}

function resolveBatch(batch: PendingCatalogInvalidation): CatalogInvalidationOptions {
  const [onlyItemId] = batch.itemIds;
  const [onlyLibraryId] = batch.libraryIds;
  return {
    itemId: batch.itemIds.size === 1 ? onlyItemId : undefined,
    libraryId: !batch.hasUnscopedEvent && batch.libraryIds.size === 1 ? onlyLibraryId : undefined,
    allowDashboardRefetch: batch.allowDashboardRefetch,
    includeLibraryLists: batch.includeLibraryLists,
  };
}

/**
 * Whether a `user_state` change can move an item in or out of a home section.
 *
 * Progress ticks arrive every few seconds throughout playback and only move a
 * position, so they must not reset the home load queue per event; they refresh
 * through `scheduleProgressHomeRefresh` instead.
 */
export function userStateChangeAffectsSectionMembership(change: string | undefined): boolean {
  return change !== "progress";
}

/**
 * One trailing home refresh per window for progress ticks. Another client's
 * playback still has to reach an OPEN home page: the first tick moves an item
 * into Continue Watching and the bar itself should advance — but Home renders
 * from its own loaded-section state, which only the refresh signal reloads.
 * A per-tick bump would reset the load queue every few seconds for the whole
 * playback; one bump per window keeps home current without that storm.
 */
export const PROGRESS_HOME_REFRESH_WINDOW_MS = 30_000;

let progressHomeRefreshTimer: number | undefined;

export function scheduleProgressHomeRefresh(
  queryClient: QueryClient,
  windowMs: number = PROGRESS_HOME_REFRESH_WINDOW_MS,
) {
  if (progressHomeRefreshTimer !== undefined) return;
  progressHomeRefreshTimer = window.setTimeout(() => {
    progressHomeRefreshTimer = undefined;
    bumpHomeRefreshSignal(queryClient);
  }, windowMs);
}
