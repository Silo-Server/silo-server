import { useMutation, useQueryClient } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { toast } from "sonner";
import {
  cancelItemDetailQueries,
  scheduleMediaSurfaceInvalidation,
  updateCatalogItemDetail,
} from "./mediaSurfaceRefresh";
import {
  PERSONAL_STATE_WRITE_TIMEOUT_MS,
  personalStateMutationOptions,
} from "./personalStateWrites";

export function useToggleWatchlist(itemId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    ...personalStateMutationOptions,
    mutationFn: (currentlyInWatchlist: boolean) =>
      currentlyInWatchlist
        ? v2("DELETE /api/v2/watchlist/{item_id}", {
            path: { item_id: itemId },
            timeoutMs: PERSONAL_STATE_WRITE_TIMEOUT_MS,
          })
        : v2("PUT /api/v2/watchlist/{item_id}", {
            path: { item_id: itemId },
            timeoutMs: PERSONAL_STATE_WRITE_TIMEOUT_MS,
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
