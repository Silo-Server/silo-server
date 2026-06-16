import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { BrowseItem } from "@/api/types";
import { hiddenKeys } from "./keys";
import { toast } from "sonner";
import { invalidateMediaSurfaceQueries, updateCatalogItemDetail } from "./mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

/**
 * Fetches the current profile's "Not Interested" (hidden) items for the
 * management page.
 */
export function useHiddenList() {
  return useQuery({
    queryKey: hiddenKeys.list(),
    queryFn: () => api<{ items: BrowseItem[] }>("/hidden").then((d) => d.items ?? []),
  });
}

/**
 * Toggles an item's hidden state. Pass the item's current `is_hidden` value to
 * the mutation: `true` un-hides (DELETE), `false` hides (PUT). Optimistically
 * updates the cached item detail and invalidates the affected media surfaces.
 */
export function useToggleHidden(itemId: string) {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (currentlyHidden: boolean) =>
      api(`/hidden/${itemId}`, {
        method: currentlyHidden ? "DELETE" : "PUT",
      }),
    onMutate: async (currentlyHidden: boolean) => {
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
          in_watchlist: detail.user_state?.in_watchlist ?? false,
          is_hidden: !currentlyHidden,
        },
      }));
      return { previous };
    },
    onError: (_err, _vars, context) => {
      for (const [queryKey, value] of context?.previous ?? []) {
        queryClient.setQueryData(queryKey, value);
      }
      toast.error("Failed to update recommendations");
    },
    onSuccess: (_data, currentlyHidden) => {
      toast.success(currentlyHidden ? "Will recommend again" : "Marked as not interested");
    },
    onSettled: async () => {
      await invalidateMediaSurfaceQueries(queryClient, { itemId });
      bumpHomeRefreshSignal(queryClient);
    },
  });
}
