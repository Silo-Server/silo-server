import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { DiscoverResponse, RecommendationSectionResponse, SectionItem } from "@/api/types";
import { recKeys } from "./keys";

interface ScoredItem {
  media_item_id: string;
  score: number;
  reason: string;
}

interface RecommendationResponse {
  items: ScoredItem[];
}

const SIMILAR_ITEMS_LIMIT = 12;

interface ForYouRow {
  type: string;
  label: string;
  cluster_index?: number;
  items: ScoredItem[];
}

interface ForYouResponse {
  rows: ForYouRow[];
}

interface ForYouMainResponse {
  row: ForYouRow | null;
}

interface TasteProfileResponse {
  top_genres: string[];
  favorite_directors: string[];
  signal_counts: Record<string, number>;
  updated_at: string;
}

export type { ScoredItem, ForYouRow, ForYouResponse };

export function useSimilarItems(itemId: string) {
  return useQuery({
    queryKey: recKeys.similar(itemId),
    queryFn: () =>
      api<RecommendationResponse>(
        `/recommendations/similar/${encodeURIComponent(itemId)}?limit=${SIMILAR_ITEMS_LIMIT}`,
      ),
    staleTime: 3600_000,
    enabled: !!itemId,
  });
}

export function useForYouMain(enabled = true) {
  return useQuery({
    queryKey: recKeys.forYouMain(),
    queryFn: () => api<ForYouMainResponse>(`/recommendations/for-you/main`),
    staleTime: 300_000,
    enabled,
  });
}

export function useForYouRows(enabled = true) {
  return useQuery({
    queryKey: recKeys.forYouRows(),
    queryFn: () => api<ForYouResponse>(`/recommendations/for-you/rows`),
    staleTime: 300_000,
    enabled,
  });
}

export function useBecauseWatched(itemId: string) {
  return useQuery({
    queryKey: recKeys.becauseWatched(itemId),
    queryFn: () => api<RecommendationResponse>(`/recommendations/because-watched/${itemId}`),
    staleTime: 300_000,
    enabled: !!itemId,
  });
}

export function useSimilarUsers(enabled = true) {
  return useQuery({
    queryKey: recKeys.similarUsers(),
    queryFn: () => api<RecommendationResponse>(`/recommendations/similar-users`),
    staleTime: 300_000,
    enabled,
  });
}

export function useTasteProfile() {
  return useQuery({
    queryKey: recKeys.tasteProfile(),
    queryFn: () => api<TasteProfileResponse>(`/recommendations/taste-profile`),
    staleTime: 300_000,
  });
}

export function usePopular(days?: number) {
  const params = days ? `?days=${days}` : "";
  return useQuery({
    queryKey: [...recKeys.all, "popular", days ?? 30],
    queryFn: () => api<RecommendationResponse>(`/recommendations/popular${params}`),
    staleTime: 600_000,
  });
}

export function useDiscover() {
  return useQuery({
    queryKey: recKeys.discover(),
    queryFn: () => api<DiscoverResponse>("/recommendations/discover"),
    staleTime: 300_000,
  });
}

export function useRecommendationSection(kind: string, key?: string) {
  const path = key
    ? `/recommendations/section/${encodeURIComponent(kind)}/${encodeURIComponent(key)}`
    : `/recommendations/section/${encodeURIComponent(kind)}`;
  return useQuery({
    queryKey: recKeys.section(kind, key),
    queryFn: () => api<RecommendationSectionResponse>(path),
    staleTime: 300_000,
    enabled: !!kind,
  });
}

export interface WatchTonightItem extends SectionItem {
  watch_tonight_source: "continue_watching" | "next_up" | "recommendation";
}

export interface WatchTonightResponse {
  items: WatchTonightItem[];
  is_cold: boolean;
}

export function useWatchTonight(enabled: boolean) {
  return useQuery({
    queryKey: recKeys.watchTonight(),
    queryFn: () => api<WatchTonightResponse>("/recommendations/watch-tonight"),
    staleTime: 0,
    enabled,
  });
}

export function useRecentlyAdded(days?: number) {
  const params = days ? `?days=${days}` : "";
  return useQuery({
    queryKey: [...recKeys.all, "recently-added", days ?? 14],
    queryFn: () => api<RecommendationResponse>(`/recommendations/recently-added${params}`),
    staleTime: 600_000,
  });
}

// --- Swipe Cards (gamified Watch Tonight) ---

export type SwipeMode = "continue" | "discover";

export interface SwipeCardCastMember {
  name: string;
  character?: string;
  photo_url?: string;
}

export interface SwipeCard extends WatchTonightItem {
  runtime?: number;
  cast: SwipeCardCastMember[];
}

export interface SwipeCardsPage {
  cards: SwipeCard[];
  has_more: boolean;
  is_cold: boolean;
}

export function useSwipeCards(enabled: boolean, mode: SwipeMode, genres: string[]) {
  return useInfiniteQuery({
    queryKey: recKeys.watchTonightCards(mode, genres),
    queryFn: ({ pageParam }: { pageParam: string[] }) => {
      const params = new URLSearchParams({ mode, limit: "12" });
      [...genres].sort().forEach((g) => params.append("genres[]", g));
      pageParam.forEach((id) => params.append("exclude_ids[]", id));
      return api<SwipeCardsPage>(`/recommendations/watch-tonight/cards?${params}`);
    },
    initialPageParam: [] as string[],
    getNextPageParam: (lastPage, allPages) => {
      if (!lastPage.has_more) return undefined;
      return allPages.flatMap((p) => p.cards.map((c) => c.content_id));
    },
    staleTime: 0,
    enabled,
  });
}
