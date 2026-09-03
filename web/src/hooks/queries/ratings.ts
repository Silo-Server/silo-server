import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type {
  CommunityRatingReaction,
  CommunityRatingsResponse,
  ItemDetail,
  RatingsCapabilities,
} from "@/api/types";
import { invalidateRatingSurfaceQueries } from "./ratingsSurfaceRefresh";
import { ratingKeys } from "./keys";
import {
  cancelItemDetailQueries,
  isItemDetailQueryKey,
  updateCatalogItemDetail,
} from "./mediaSurfaceRefresh";

export function useSetRating(itemId: string) {
  const queryClient = useQueryClient();
  const communityQueryKey = ratingKeys.community(itemId);
  return useMutation({
    scope: { id: `rating:${itemId}` },
    mutationFn: (rating: number) =>
      api(`/ratings/${itemId}`, {
        method: "PUT",
        body: JSON.stringify({ rating }),
      }),
    onMutate: async (rating: number) => {
      await Promise.all([
        cancelItemDetailQueries(queryClient, itemId),
        queryClient.cancelQueries({ queryKey: communityQueryKey }),
      ]);
      const previous = queryClient.getQueriesData<ItemDetail>({
        predicate: (query) => isItemDetailQueryKey(query.queryKey, itemId),
      });
      const previousCommunity =
        queryClient.getQueryData<CommunityRatingsResponse>(communityQueryKey);
      updateCatalogItemDetail(queryClient, itemId, (detail) => ({
        ...detail,
        user_rating: rating,
      }));
      queryClient.setQueryData<CommunityRatingsResponse>(communityQueryKey, (current) =>
        updateViewerCommunityRating(current, rating, new Date().toISOString()),
      );
      return { previous, previousCommunity };
    },
    onError: (_err, _vars, context) => {
      for (const [queryKey, value] of context?.previous ?? []) {
        queryClient.setQueryData(queryKey, value);
      }
      if (context) {
        queryClient.setQueryData(communityQueryKey, context.previousCommunity);
      }
    },
    onSettled: () => {
      return invalidateRatingSurfaceQueries(queryClient, itemId);
    },
  });
}

export function useDeleteRating(itemId: string) {
  const queryClient = useQueryClient();
  const communityQueryKey = ratingKeys.community(itemId);
  return useMutation({
    scope: { id: `rating:${itemId}` },
    mutationFn: () => api(`/ratings/${itemId}`, { method: "DELETE" }),
    onMutate: async () => {
      await Promise.all([
        cancelItemDetailQueries(queryClient, itemId),
        queryClient.cancelQueries({ queryKey: communityQueryKey }),
      ]);
      const previous = queryClient.getQueriesData<ItemDetail>({
        predicate: (query) => isItemDetailQueryKey(query.queryKey, itemId),
      });
      const previousCommunity =
        queryClient.getQueryData<CommunityRatingsResponse>(communityQueryKey);
      updateCatalogItemDetail(queryClient, itemId, (detail) => ({
        ...detail,
        user_rating: null,
      }));
      queryClient.setQueryData<CommunityRatingsResponse>(communityQueryKey, (current) =>
        removeViewerCommunityRating(current),
      );
      return { previous, previousCommunity };
    },
    onError: (_err, _vars, context) => {
      for (const [queryKey, value] of context?.previous ?? []) {
        queryClient.setQueryData(queryKey, value);
      }
      if (context) {
        queryClient.setQueryData(communityQueryKey, context.previousCommunity);
      }
    },
    onSettled: () => {
      return invalidateRatingSurfaceQueries(queryClient, itemId);
    },
  });
}

export function useRatingsCapabilities() {
  return useQuery({
    queryKey: ratingKeys.capabilities(),
    queryFn: () => api<RatingsCapabilities>("/ratings/capabilities"),
    staleTime: Infinity,
    gcTime: Infinity,
  });
}

export function useCommunityRatings(itemId: string) {
  const capabilitiesQuery = useRatingsCapabilities();
  const communityQuery = useQuery({
    queryKey: ratingKeys.community(itemId),
    queryFn: () => api<CommunityRatingsResponse>(`/ratings/${itemId}/community?limit=100`),
    enabled: itemId.length > 0 && capabilitiesQuery.data?.community_ratings === true,
    staleTime: 30_000,
  });
  return {
    ...communityQuery,
    capabilities: capabilitiesQuery.data,
    isError: capabilitiesQuery.isError || communityQuery.isError,
    isLoading: capabilitiesQuery.isLoading || communityQuery.isLoading,
  };
}

interface CommunityReactionMutation {
  ratingKey: string;
  reaction: CommunityRatingReaction | null;
}

function updateViewerCommunityRating(
  current: CommunityRatingsResponse | undefined,
  rating: number,
  ratedAt: string,
) {
  if (!current) return current;
  const viewer = current.ratings.find((entry) => entry.is_viewer);
  if (!viewer) return current;

  const average =
    current.average_rating == null || current.vote_count === 0
      ? current.average_rating
      : (current.average_rating * current.vote_count - viewer.rating + rating) / current.vote_count;
  return {
    ...current,
    average_rating: average,
    ratings: current.ratings.map((entry) =>
      entry.is_viewer ? { ...entry, rating, rated_at: ratedAt } : entry,
    ),
  };
}

function removeViewerCommunityRating(current: CommunityRatingsResponse | undefined) {
  if (!current) return current;
  const viewer = current.ratings.find((entry) => entry.is_viewer);
  if (!viewer) return current;

  const voteCount = Math.max(0, current.vote_count - 1);
  const average =
    voteCount === 0 || current.average_rating == null
      ? null
      : (current.average_rating * current.vote_count - viewer.rating) / voteCount;
  return {
    ...current,
    average_rating: average,
    vote_count: voteCount,
    ratings: current.ratings.filter((entry) => !entry.is_viewer),
  };
}

export function useSetCommunityRatingReaction(itemId: string) {
  const queryClient = useQueryClient();
  const queryKey = ratingKeys.community(itemId);

  return useMutation({
    mutationFn: ({ ratingKey, reaction }: CommunityReactionMutation) => {
      const path = `/ratings/${itemId}/community/${encodeURIComponent(ratingKey)}/reaction`;
      if (reaction === null) {
        return api<void>(path, { method: "DELETE" });
      }
      return api<void>(path, {
        method: "PUT",
        body: JSON.stringify({ reaction }),
      });
    },
    onMutate: async ({ ratingKey, reaction }: CommunityReactionMutation) => {
      await queryClient.cancelQueries({ queryKey });
      const previous = queryClient.getQueryData<CommunityRatingsResponse>(queryKey);
      queryClient.setQueryData<CommunityRatingsResponse>(queryKey, (current) => {
        if (!current) return current;
        return {
          ...current,
          ratings: current.ratings.map((entry) => {
            if (entry.key !== ratingKey) return entry;
            return {
              ...entry,
              up_count: Math.max(
                0,
                entry.up_count -
                  (entry.viewer_reaction === "up" ? 1 : 0) +
                  (reaction === "up" ? 1 : 0),
              ),
              down_count: Math.max(
                0,
                entry.down_count -
                  (entry.viewer_reaction === "down" ? 1 : 0) +
                  (reaction === "down" ? 1 : 0),
              ),
              viewer_reaction: reaction ?? undefined,
            };
          }),
        };
      });
      return { previous };
    },
    onError: (_error, _variables, context) => {
      if (context?.previous) {
        queryClient.setQueryData(queryKey, context.previous);
      }
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey }),
  });
}
