import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { ItemDetail } from "@/api/types";
import { v2 } from "@/api/v2/request";
import { invalidateRatingSurfaceQueries } from "./ratingsSurfaceRefresh";
import {
  cancelItemDetailQueries,
  isItemDetailQueryKey,
  updateCatalogItemDetail,
} from "./mediaSurfaceRefresh";
import {
  PERSONAL_STATE_WRITE_TIMEOUT_MS,
  personalStateMutationOptions,
} from "./personalStateWrites";

export function useSetRating(itemId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    ...personalStateMutationOptions,
    mutationFn: (rating: number) =>
      v2("PUT /api/v2/ratings/{item_id}", {
        path: { item_id: itemId },
        body: { rating },
        timeoutMs: PERSONAL_STATE_WRITE_TIMEOUT_MS,
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
      toast.error("Failed to update rating");
    },
    onSettled: () => {
      return invalidateRatingSurfaceQueries(queryClient, itemId);
    },
  });
}

export function useDeleteRating(itemId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    ...personalStateMutationOptions,
    mutationFn: () =>
      v2("DELETE /api/v2/ratings/{item_id}", {
        path: { item_id: itemId },
        timeoutMs: PERSONAL_STATE_WRITE_TIMEOUT_MS,
      }),
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
      toast.error("Failed to update rating");
    },
    onSettled: () => {
      return invalidateRatingSurfaceQueries(queryClient, itemId);
    },
  });
}
