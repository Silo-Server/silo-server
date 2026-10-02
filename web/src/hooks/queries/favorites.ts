import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import type { BrowseItem } from "@/api/types";
import { catalogItemFromV2 } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { favoriteKeys } from "./keys";
import { toast } from "sonner";
import {
  cancelItemDetailQueries,
  scheduleMediaSurfaceInvalidation,
  updateCatalogItemDetail,
} from "./mediaSurfaceRefresh";

export function useFavorites() {
  return useQuery({
    queryKey: favoriteKeys.list(),
    queryFn: ({ signal }): Promise<BrowseItem[]> =>
      v2("GET /api/v2/favorites", { signal }).then((data) => data.items.map(catalogItemFromV2)),
  });
}

/**
 * Whether the acting profile has any favorite, for surfaces that only need
 * that bit (the taste-seed gate and banner). It asks for a one-card page
 * rather than the full list. An empty page that still carries a cursor means
 * the newest favorite is one the viewer may not see and more follow; the
 * profile has favorited something, so it counts as having favorites.
 */
export function useHasFavorites() {
  return useQuery({
    queryKey: favoriteKeys.exists(),
    queryFn: ({ signal }): Promise<boolean> =>
      v2("GET /api/v2/favorites", { query: { limit: 1 }, signal }).then(
        (data) => data.items.length > 0 || data.page?.has_more === true,
      ),
  });
}

export function useToggleFavorite(itemId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (currentlyFavorite: boolean) =>
      currentlyFavorite
        ? v2("DELETE /api/v2/favorites/{item_id}", { path: { item_id: itemId } })
        : v2("PUT /api/v2/favorites/{item_id}", { path: { item_id: itemId } }),
    onMutate: async (currentlyFavorite: boolean) => {
      await cancelItemDetailQueries(queryClient, itemId);
      updateCatalogItemDetail(queryClient, itemId, (detail) => ({
        ...detail,
        user_state: {
          played: detail.user_state?.played ?? detail.user_data?.played ?? false,
          is_favorite: !currentlyFavorite,
          in_watchlist: detail.user_state?.in_watchlist ?? false,
        },
      }));
    },
    // Revert only this mutation's own field. Restoring a whole snapshot would
    // discard a concurrent watchlist/watched toggle's optimistic state.
    onError: (_err, currentlyFavorite) => {
      updateCatalogItemDetail(queryClient, itemId, (detail) => ({
        ...detail,
        user_state: {
          played: detail.user_state?.played ?? detail.user_data?.played ?? false,
          is_favorite: currentlyFavorite,
          in_watchlist: detail.user_state?.in_watchlist ?? false,
        },
      }));
      toast.error("Failed to update favorites");
    },
    onSuccess: (_data, currentlyFavorite) => {
      toast.success(currentlyFavorite ? "Removed from favorites" : "Added to favorites");
    },
    onSettled: () => {
      scheduleMediaSurfaceInvalidation(queryClient, {
        itemId,
        skipItemDetail: true,
        skipSimilarItems: true,
      });
    },
  });
}
