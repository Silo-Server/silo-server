import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { BrowseItem } from "@/api/types";
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
    queryFn: () => api<{ items: BrowseItem[] }>("/favorites").then((d) => d.items ?? []),
  });
}

export function useToggleFavorite(itemId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (currentlyFavorite: boolean) =>
      api(`/favorites/${itemId}`, {
        method: currentlyFavorite ? "DELETE" : "PUT",
      }),
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
