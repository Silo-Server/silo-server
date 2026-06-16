import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { BrowseItem } from "@/api/types";
import { watchlistKeys } from "./keys";
import { toast } from "sonner";
import { invalidateMediaSurfaceQueries, updateCatalogItemDetail } from "./mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

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
      await queryClient.cancelQueries({ queryKey: ["catalog", "items", itemId, "detail"] });
      const previous = queryClient.getQueriesData({
        predicate: (query) =>
          Array.isArray(query.queryKey) &&
          query.queryKey[0] === "catalog" &&
          query.queryKey[1] === "items" &&
          query.queryKey[2] === itemId &&
          query.queryKey[3] === "detail",
      });
      updateCatalogItemDetail(queryClient, itemId, (detail) => ({
        ...detail,
        user_state: {
          played: detail.user_state?.played ?? false,
          is_favorite: detail.user_state?.is_favorite ?? false,
          in_watchlist: !currentlyInWatchlist,
          is_hidden: detail.user_state?.is_hidden ?? false,
        },
      }));
      return { previous };
    },
    onError: (_err, _vars, context) => {
      for (const [queryKey, value] of context?.previous ?? []) {
        queryClient.setQueryData(queryKey, value);
      }
      toast.error("Failed to update watchlist");
    },
    onSuccess: (_data, currentlyInWatchlist) => {
      toast.success(currentlyInWatchlist ? "Removed from watchlist" : "Added to watchlist");
    },
    onSettled: async () => {
      await invalidateMediaSurfaceQueries(queryClient, { itemId });
      bumpHomeRefreshSignal(queryClient);
    },
  });
}
