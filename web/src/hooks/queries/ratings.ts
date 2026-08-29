import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { ItemDetail } from "@/api/types";
import { invalidateRatingSurfaceQueries } from "./ratingsSurfaceRefresh";
import {
  cancelItemDetailQueries,
  isItemDetailQueryKey,
  updateCatalogItemDetail,
} from "./mediaSurfaceRefresh";

export function useSetRating(itemId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (rating: number) =>
      api(`/ratings/${itemId}`, {
        method: "PUT",
        body: JSON.stringify({ rating }),
      }),
    onMutate: async (rating: number) => {
      await cancelItemDetailQueries(queryClient, itemId);
      const previous = queryClient.getQueriesData<ItemDetail>({
        predicate: (query) => isItemDetailQueryKey(query.queryKey, itemId),
      });
      updateCatalogItemDetail(queryClient, itemId, (detail) => ({
        ...detail,
        user_rating: rating,
      }));
      return { previous };
    },
    onError: (_err, _vars, context) => {
      for (const [queryKey, value] of context?.previous ?? []) {
        queryClient.setQueryData(queryKey, value);
      }
    },
    onSettled: () => {
      return invalidateRatingSurfaceQueries(queryClient, itemId);
    },
  });
}

export function useDeleteRating(itemId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api(`/ratings/${itemId}`, { method: "DELETE" }),
    onMutate: async () => {
      await cancelItemDetailQueries(queryClient, itemId);
      const previous = queryClient.getQueriesData<ItemDetail>({
        predicate: (query) => isItemDetailQueryKey(query.queryKey, itemId),
      });
      updateCatalogItemDetail(queryClient, itemId, (detail) => ({
        ...detail,
        user_rating: null,
      }));
      return { previous };
    },
    onError: (_err, _vars, context) => {
      for (const [queryKey, value] of context?.previous ?? []) {
        queryClient.setQueryData(queryKey, value);
      }
    },
    onSettled: () => {
      return invalidateRatingSurfaceQueries(queryClient, itemId);
    },
  });
}
