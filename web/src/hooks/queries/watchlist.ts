import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { BrowseItem } from "@/api/types";
import { watchlistKeys } from "./keys";
import { toast } from "sonner";
import {
  cancelItemDetailQueries,
  scheduleMediaSurfaceInvalidation,
  updateCatalogItemDetail,
} from "./mediaSurfaceRefresh";

export function useWatchlist() {
  return useQuery({
    queryKey: watchlistKeys.list(),
    queryFn: () => api<{ items: BrowseItem[] }>("/watchlist").then((d) => d.items ?? []),
  });
}

export function useToggleWatchlist(itemId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (currentlyInWatchlist: boolean) =>
      api(`/watchlist/${itemId}`, {
        method: currentlyInWatchlist ? "DELETE" : "PUT",
      }),
    onMutate: async (currentlyInWatchlist: boolean) => {
      await cancelItemDetailQueries(queryClient, itemId);
      updateCatalogItemDetail(queryClient, itemId, (detail) => ({
        ...detail,
        user_state: {
          played: detail.user_state?.played ?? detail.user_data?.played ?? false,
          is_favorite: detail.user_state?.is_favorite ?? false,
          in_watchlist: !currentlyInWatchlist,
        },
      }));
    },
    // Revert only this mutation's own field. Restoring a whole snapshot would
    // discard a concurrent favorite/watched toggle's optimistic state.
    onError: (_err, currentlyInWatchlist) => {
      updateCatalogItemDetail(queryClient, itemId, (detail) => ({
        ...detail,
        user_state: {
          played: detail.user_state?.played ?? detail.user_data?.played ?? false,
          is_favorite: detail.user_state?.is_favorite ?? false,
          in_watchlist: currentlyInWatchlist,
        },
      }));
      toast.error("Failed to update watchlist");
    },
    onSuccess: (_data, currentlyInWatchlist) => {
      toast.success(currentlyInWatchlist ? "Removed from watchlist" : "Added to watchlist");
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
