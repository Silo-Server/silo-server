import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type { QueryClient } from "@tanstack/react-query";
import { catalogItemFromV2, type CatalogCardItem } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { recKeys, favoriteKeys } from "./keys";
import { invalidateMediaSurfaceQueries } from "./mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";
import { storage } from "@/utils/storage";

export interface TasteSeedItemsPage {
  items: CatalogCardItem[];
  next_cursor?: string;
}

const TASTE_SEED_PAGE_SIZE = 30;

/**
 * Fetches catalog posters for the taste-seeding picker. Blends server engagement
 * with rating reliability and recency so even a fresh server with no watch
 * history surfaces meaningful posters. Items already favorited by the profile
 * carry user_state.is_favorite=true so the UI can pre-select them.
 */
export function useTasteSeedItems(enabled = true) {
  return useInfiniteQuery({
    queryKey: recKeys.tasteSeedItems(),
    queryFn: ({ pageParam, signal }): Promise<TasteSeedItemsPage> =>
      v2("GET /api/v2/recommendations/taste-seed/items", {
        query: { limit: TASTE_SEED_PAGE_SIZE, cursor: pageParam || undefined },
        signal,
      }).then((page) => ({
        items: page.items.map(catalogItemFromV2),
        next_cursor: page.page?.next_cursor,
      })),
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    staleTime: 600_000,
    enabled,
  });
}

/**
 * When the recommendation surfaces are refetched again after a taste-seed
 * submission. The server answers once the profile's refresh is done or after
 * a few seconds; a slower refresh lands later, and these refetches pick it up.
 * A submission that arrives while a refresh is reading makes that refresh run
 * once more when it ends, each run within the server's two-minute timeout, so
 * the last refetch comes after two timeouts, when neither can be running.
 */
const TASTE_SEED_REFETCH_DELAYS_MS = [3_000, 15_000, 45_000, 125_000, 245_000] as const;

/** The pending delayed refetches of the latest submission. */
let tasteSeedRefetchTimers: ReturnType<typeof setTimeout>[] = [];

function cancelTasteSeedRefetches() {
  for (const timer of tasteSeedRefetchTimers) clearTimeout(timer);
  tasteSeedRefetchTimers = [];
}

/**
 * Schedules the delayed refetches for the current profile, replacing those of
 * an earlier submission. Each refetches only the recommendation reads, which
 * reload just the queries on screen; Home, which reloads every section on a
 * bump, is bumped once more with the last. A profile or account switch
 * cancels what is left: the refetches would only reload the new profile's
 * rows.
 */
function scheduleTasteSeedRefetches(queryClient: QueryClient) {
  cancelTasteSeedRefetches();
  const profileId = storage.get(storage.KEYS.PROFILE_ID);
  const last = TASTE_SEED_REFETCH_DELAYS_MS.length - 1;
  tasteSeedRefetchTimers = TASTE_SEED_REFETCH_DELAYS_MS.map((delay, i) =>
    setTimeout(() => {
      if (storage.get(storage.KEYS.PROFILE_ID) !== profileId) {
        cancelTasteSeedRefetches();
        return;
      }
      void queryClient.invalidateQueries({ queryKey: recKeys.all });
      if (i === last) {
        bumpHomeRefreshSignal(queryClient);
        tasteSeedRefetchTimers = [];
      }
    }, delay),
  );
}

/**
 * Submits a batch of selected content IDs as favorites and triggers a single
 * taste-profile refresh. Uses the dedicated POST /api/v2/recommendations/taste-seed
 * endpoint so the server can debounce the refresh into one request rather than
 * one-per-favorite.
 */
export function useSubmitTasteSeed() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (itemIds: string[]) =>
      v2("POST /api/v2/recommendations/taste-seed", { body: { item_ids: itemIds } }),
    onSuccess: async () => {
      // Invalidate favorites and recommendation surfaces. The server waits
      // for the profile's refresh before answering, so the rows usually
      // already reflect the picks; the delayed refetches cover a refresh that
      // took longer than that wait.
      await queryClient.invalidateQueries({ queryKey: favoriteKeys.all });
      await queryClient.invalidateQueries({ queryKey: recKeys.all });
      await invalidateMediaSurfaceQueries(queryClient);
      bumpHomeRefreshSignal(queryClient);
      scheduleTasteSeedRefetches(queryClient);
    },
  });
}
