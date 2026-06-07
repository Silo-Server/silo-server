import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { SectionItem } from "@/api/types";
import { recKeys, favoriteKeys } from "./keys";
import { invalidateMediaSurfaceQueries } from "./mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

export interface TasteSeedItemsPage {
  items: SectionItem[];
  next_offset?: number;
}

interface TasteSeedSubmitResponse {
  added: number;
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
    queryFn: ({ pageParam }: { pageParam: number }) =>
      api<TasteSeedItemsPage>(
        `/recommendations/taste-seed/items?limit=${TASTE_SEED_PAGE_SIZE}&offset=${pageParam}`,
      ),
    initialPageParam: 0,
    getNextPageParam: (lastPage) => lastPage.next_offset ?? undefined,
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
      api<TasteSeedSubmitResponse>("/recommendations/taste-seed", {
        method: "POST",
        body: JSON.stringify({ item_ids: itemIds }),
      }),
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
