import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { catalogItemFromV2, type CatalogCardItem } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { recKeys, favoriteKeys } from "./keys";
import { invalidateMediaSurfaceQueries } from "./mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

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
 * Submits a batch of selected content IDs as favorites and triggers a single
 * taste-profile refresh. Uses the dedicated POST /recommendations/taste-seed
 * endpoint so the server can debounce the refresh into one request rather than
 * one-per-favorite.
 */
export function useSubmitTasteSeed() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (itemIds: string[]) =>
      v2("POST /api/v2/recommendations/taste-seed", { body: { item_ids: itemIds } }),
    onSuccess: async () => {
      // Invalidate favorites and recommendation surfaces — the new favorites
      // should appear immediately; the For You / Discover rows will warm up
      // on next render once the worker re-runs the taste profile.
      await queryClient.invalidateQueries({ queryKey: favoriteKeys.all });
      await queryClient.invalidateQueries({ queryKey: recKeys.all });
      await invalidateMediaSurfaceQueries(queryClient);
      bumpHomeRefreshSignal(queryClient);
    },
  });
}
