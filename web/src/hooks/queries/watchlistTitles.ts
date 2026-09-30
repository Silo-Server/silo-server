import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { RequestMediaType, RequestState } from "@/api/types";
import {
  addWatchlistTitleV2,
  deleteWatchlistTitleV2,
  listWatchlistTitlesV2,
  type WatchlistTitle,
  type WatchlistTitleEntry,
} from "@/api/v2/watchlistTitles";
import { formatRequestReason } from "@/lib/mediaRequests";
import { catalogKeys, requestKeys, watchlistKeys } from "./keys";
import { scheduleMediaSurfaceInvalidation } from "./mediaSurfaceRefresh";
import {
  invalidateRequestSurfaces,
  REQUEST_DOWNLOAD_REFETCH_INTERVAL,
  REQUESTS_STALE_TIME,
} from "./useRequests";

/** Watchlist entries for titles the library doesn't have yet. */
export function useWatchlistTitles(options: { enabled?: boolean } = {}) {
  const queryClient = useQueryClient();
  return useQuery({
    queryKey: watchlistKeys.titles(),
    queryFn: async ({ signal }) => {
      const before = queryClient.getQueryData<WatchlistTitle[]>(watchlistKeys.titles());
      const titles = await listWatchlistTitlesV2(signal);
      refreshAfterTitlesRead(queryClient, before, titles);
      return titles;
    },
    enabled: options.enabled ?? true,
    staleTime: REQUESTS_STALE_TIME,
    refetchInterval: (query) => watchlistTitlesRefetchInterval(query.state.data),
  });
}

/**
 * While a title downloads, the list refreshes on the interval the other
 * request surfaces use, so its progress doesn't freeze.
 */
export function watchlistTitlesRefetchInterval(
  titles: readonly WatchlistTitle[] | undefined,
): number | false {
  return titles?.some((t) => t.request.download) ? REQUEST_DOWNLOAD_REFETCH_INTERVAL : false;
}

/**
 * Reading the titles moves the ones the library now has onto the library
 * watchlist, server side, so the library tab's grid is refreshed after every
 * read. When a title this tab showed is gone, the library surfaces that list
 * the watchlist (the home row, item states) refresh too. That refresh reads
 * the titles once more, which finds nothing else gone, so it settles.
 */
export function refreshAfterTitlesRead(
  queryClient: QueryClient,
  before: readonly WatchlistTitle[] | undefined,
  after: readonly WatchlistTitle[],
): void {
  queryClient.invalidateQueries({ predicate: (query) => isWatchlistCatalogQuery(query.queryKey) });
  if (!before) return;
  const still = new Set(after.map((t) => `${t.media_type}-${t.tmdb_id}`));
  if (before.some((t) => !still.has(`${t.media_type}-${t.tmdb_id}`))) {
    scheduleMediaSurfaceInvalidation(queryClient);
  }
}

export interface ToggleWatchlistTitleInput {
  mediaType: RequestMediaType;
  tmdbID: number;
  /** Names the title in the toast. */
  title: string;
  /** Whether the title is on the watchlist now; the mutation flips it. */
  inWatchlist: boolean;
  /** The title's request state before the add, to tell what the add did. */
  request?: RequestState;
}

/** The toast after an add: whether the title was also requested, and why not. */
export function watchlistAddToast(
  entry: Pick<WatchlistTitleEntry, "item_id" | "request">,
  input: Pick<ToggleWatchlistTitleInput, "title" | "request">,
  watchlistRequests: boolean | undefined,
): { title: string; description?: string } {
  const before = input.request;
  const after = entry.request;
  const available = `We'll let you know when ${input.title} is available.`;
  if (entry.item_id) {
    return { title: "Added to your watchlist" };
  }
  if (after.requested_by_viewer && !before?.requested_by_viewer) {
    return {
      title: "Added to your watchlist and requested",
      description: `${available} It moves into your watchlist on its own.`,
    };
  }
  if (after.following && !before?.following) {
    return { title: "Added to your watchlist", description: available };
  }
  if (after.status) {
    // It already had a request the viewer made or follows.
    return { title: "Added to your watchlist" };
  }
  if (after.reason) {
    return {
      title: "Added to your watchlist",
      description: `It wasn't requested: ${formatRequestReason(after.reason).toLowerCase()}.`,
    };
  }
  if (watchlistRequests === false) {
    return {
      title: "Added to your watchlist",
      description: "It wasn't requested, because watchlist requests are off.",
    };
  }
  return { title: "Added to your watchlist", description: "It wasn't requested." };
}

/**
 * Sets in_watchlist on every cached TMDB title matching the key: the title
 * page, and the Discover, browse and search results that list it. The request
 * caches hold those titles at several depths (pages, sections, the title
 * page's recommendations), so this walks plain objects and arrays, and keeps
 * the reference of anything it does not change.
 */
export function patchCachedInWatchlist(
  data: unknown,
  mediaType: RequestMediaType,
  tmdbID: number,
  inWatchlist: boolean,
): unknown {
  if (Array.isArray(data)) {
    let changed = false;
    const next = data.map((entry) => {
      const patched = patchCachedInWatchlist(entry, mediaType, tmdbID, inWatchlist);
      if (patched !== entry) changed = true;
      return patched;
    });
    return changed ? next : data;
  }
  if (
    typeof data !== "object" ||
    data === null ||
    Object.getPrototypeOf(data) !== Object.prototype
  ) {
    return data;
  }
  const record = data as Record<string, unknown>;
  let next: Record<string, unknown> | null = null;
  for (const [key, value] of Object.entries(record)) {
    if (typeof value !== "object" || value === null) continue;
    const patched = patchCachedInWatchlist(value, mediaType, tmdbID, inWatchlist);
    if (patched !== value) {
      next ??= { ...record };
      next[key] = patched;
    }
  }
  const isTitle =
    record.media_type === mediaType &&
    record.tmdb_id === tmdbID &&
    typeof record.request === "object" &&
    record.request !== null;
  if (isTitle && record.in_watchlist !== inWatchlist) {
    next ??= { ...record };
    next.in_watchlist = inWatchlist;
  }
  return next ?? data;
}

function setCachedInWatchlist(
  queryClient: QueryClient,
  mediaType: RequestMediaType,
  tmdbID: number,
  inWatchlist: boolean,
) {
  queryClient.setQueriesData({ queryKey: requestKeys.all }, (data: unknown) =>
    patchCachedInWatchlist(data, mediaType, tmdbID, inWatchlist),
  );
}

function isWatchlistCatalogQuery(queryKey: readonly unknown[]): boolean {
  const [root, kind, params] = queryKey;
  return (
    root === catalogKeys.all[0] &&
    (kind === "list" || kind === "filters") &&
    typeof params === "object" &&
    params !== null &&
    (params as { source?: unknown }).source === "watchlist"
  );
}

/**
 * Adds a title to the watchlist by its TMDB ID, or removes it. The server may
 * also request the title on add, or cancel its watchlist request on remove,
 * so both refresh the request surfaces as well as the watchlist.
 */
export function useToggleWatchlistTitle() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async (input: ToggleWatchlistTitleInput): Promise<WatchlistTitleEntry | null> => {
      if (input.inWatchlist) {
        await deleteWatchlistTitleV2(input.mediaType, input.tmdbID);
        return null;
      }
      return addWatchlistTitleV2(input.mediaType, input.tmdbID);
    },
    onMutate: async ({ mediaType, tmdbID, inWatchlist }) => {
      await queryClient.cancelQueries({ queryKey: requestKeys.detail(mediaType, tmdbID) });
      setCachedInWatchlist(queryClient, mediaType, tmdbID, !inWatchlist);
    },
    onError: (err, { mediaType, tmdbID, inWatchlist }) => {
      setCachedInWatchlist(queryClient, mediaType, tmdbID, inWatchlist);
      toast.error(err instanceof Error ? err.message : "Failed to update your watchlist");
    },
    onSuccess: (entry, input) => {
      if (!entry) {
        toast.success("Removed from your watchlist");
        return;
      }
      const status = queryClient.getQueryData<{ watchlist_requests?: boolean }>(
        requestKeys.status(),
      );
      const message = watchlistAddToast(entry, input, status?.watchlist_requests);
      toast.success(
        message.title,
        message.description ? { description: message.description } : undefined,
      );
    },
    onSettled: (entry, _err, input) => {
      queryClient.invalidateQueries({ queryKey: watchlistKeys.all });
      queryClient.invalidateQueries({
        predicate: (query) => isWatchlistCatalogQuery(query.queryKey),
      });
      invalidateRequestSurfaces(queryClient);
      // An add the library has lands on its item, and a remove drops the
      // item's entry too, so the library surfaces (the home watchlist row,
      // the item's user state) refresh as for a library watchlist toggle.
      if (entry?.item_id) {
        scheduleMediaSurfaceInvalidation(queryClient, { itemId: entry.item_id });
      } else if (input.inWatchlist) {
        scheduleMediaSurfaceInvalidation(queryClient);
      }
    },
  });
}
