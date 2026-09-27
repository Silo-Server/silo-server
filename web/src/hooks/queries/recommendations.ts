import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { catalogItemFromV2, type CatalogCardItem } from "@/api/v2/catalog";
import {
  type DiscoverResponse,
  type RecommendationSectionResponse,
  discoverFromV2,
  discoverRowFromV2,
  recommendationSectionFromV2,
  swipeCardsPageFromV2,
  watchTonightFromV2,
} from "@/api/v2/recommendations";
import { v2, type V2PathParams, type V2Result } from "@/api/v2/request";
import { recKeys } from "./keys";

const SIMILAR_ITEMS_LIMIT = 12;

/** A recommendation list: the recommended cards, best first. */
export interface RecommendationResponse {
  items: CatalogCardItem[];
}

export type TasteProfileResponse = V2Result<"GET /api/v2/recommendations/taste-profile">;

function cardList(body: {
  items: Parameters<typeof catalogItemFromV2>[0][];
}): RecommendationResponse {
  return { items: body.items.map(catalogItemFromV2) };
}

export function useSimilarItems(itemId: string) {
  return useQuery({
    queryKey: recKeys.similar(itemId),
    queryFn: ({ signal }): Promise<RecommendationResponse> =>
      v2("GET /api/v2/recommendations/similar/{item_id}", {
        path: { item_id: itemId },
        query: { limit: SIMILAR_ITEMS_LIMIT },
        signal,
      }).then(cardList),
    staleTime: 3600_000,
    enabled: !!itemId,
  });
}

export function useForYouMain(enabled = true) {
  return useQuery({
    queryKey: recKeys.forYouMain(),
    queryFn: ({ signal }) =>
      v2("GET /api/v2/recommendations/for-you/main", { signal }).then(discoverRowFromV2),
    staleTime: 300_000,
    enabled,
  });
}

export function useForYouRows(enabled = true) {
  return useQuery({
    queryKey: recKeys.forYouRows(),
    queryFn: ({ signal }): Promise<DiscoverResponse> =>
      v2("GET /api/v2/recommendations/for-you/rows", { signal }).then(discoverFromV2),
    staleTime: 300_000,
    enabled,
  });
}

export function useBecauseWatched(itemId: string) {
  return useQuery({
    queryKey: recKeys.becauseWatched(itemId),
    queryFn: ({ signal }): Promise<RecommendationResponse> =>
      v2("GET /api/v2/recommendations/because-watched/{item_id}", {
        path: { item_id: itemId },
        signal,
      }).then(cardList),
    staleTime: 300_000,
    enabled: !!itemId,
  });
}

export function useSimilarUsers(enabled = true) {
  return useQuery({
    queryKey: recKeys.similarUsers(),
    queryFn: ({ signal }): Promise<RecommendationResponse> =>
      v2("GET /api/v2/recommendations/similar-users", { signal }).then(cardList),
    staleTime: 300_000,
    enabled,
  });
}

export function useTasteProfile() {
  return useQuery({
    queryKey: recKeys.tasteProfile(),
    queryFn: ({ signal }): Promise<TasteProfileResponse> =>
      v2("GET /api/v2/recommendations/taste-profile", { signal }),
    staleTime: 300_000,
  });
}

export function usePopular(days?: number) {
  return useQuery({
    queryKey: [...recKeys.all, "popular", days ?? 30],
    queryFn: ({ signal }): Promise<RecommendationResponse> =>
      v2("GET /api/v2/recommendations/popular", { query: { days }, signal }).then(cardList),
    staleTime: 600_000,
  });
}

export function useDiscover() {
  return useQuery({
    queryKey: recKeys.discover(),
    queryFn: ({ signal }): Promise<DiscoverResponse> =>
      v2("GET /api/v2/recommendations/discover", { signal }).then(discoverFromV2),
    staleTime: 300_000,
  });
}

/** The section kinds the contract serves; a discover row's `section_kind` is one of these. */
export type RecommendationSectionKind =
  V2PathParams<"GET /api/v2/recommendations/section/{kind}">["kind"];

const RECOMMENDATION_SECTION_KINDS: readonly RecommendationSectionKind[] = [
  "for-you-main",
  "cluster",
  "similar-users",
  "popular",
  "recently-added",
  "top-rated",
  "genre",
];

export function isRecommendationSectionKind(
  kind: string | undefined,
): kind is RecommendationSectionKind {
  return (RECOMMENDATION_SECTION_KINDS as readonly string[]).includes(kind ?? "");
}

/** Reads one section; an unknown `kind` (a hand-typed URL) leaves the query disabled. */
export function useRecommendationSection(kind: string | undefined, key?: string) {
  const sectionKind = isRecommendationSectionKind(kind) ? kind : undefined;
  return useQuery({
    queryKey: recKeys.section(kind ?? "", key),
    queryFn: ({ signal }): Promise<RecommendationSectionResponse> =>
      v2("GET /api/v2/recommendations/section/{kind}", {
        path: { kind: sectionKind ?? "for-you-main" },
        query: { key },
        signal,
      }).then(recommendationSectionFromV2),
    staleTime: 300_000,
    enabled: sectionKind !== undefined,
  });
}

export type WatchTonightResponse = ReturnType<typeof watchTonightFromV2>;
export type WatchTonightItem = WatchTonightResponse["items"][number];

export function useWatchTonight(enabled: boolean) {
  return useQuery({
    queryKey: recKeys.watchTonight(),
    queryFn: ({ signal }): Promise<WatchTonightResponse> =>
      v2("GET /api/v2/recommendations/watch-tonight", { signal }).then(watchTonightFromV2),
    staleTime: 0,
    enabled,
  });
}

export function useRecentlyAdded(days?: number) {
  return useQuery({
    queryKey: [...recKeys.all, "recently-added", days ?? 14],
    queryFn: ({ signal }): Promise<RecommendationResponse> =>
      v2("GET /api/v2/recommendations/recently-added", { query: { days }, signal }).then(cardList),
    staleTime: 600_000,
  });
}

// --- Swipe Cards (gamified Watch Tonight) ---

export type SwipeMode = "continue" | "discover";

export type SwipeCardsPage = ReturnType<typeof swipeCardsPageFromV2>;
export type SwipeCard = SwipeCardsPage["cards"][number];
export type SwipeCardCastMember = SwipeCard["cast"][number];

export function useSwipeCards(enabled: boolean, mode: SwipeMode, genres: string[]) {
  return useInfiniteQuery({
    queryKey: recKeys.watchTonightCards(mode, genres),
    queryFn: ({ pageParam, signal }: { pageParam: string[]; signal?: AbortSignal }) =>
      v2("GET /api/v2/recommendations/watch-tonight/cards", {
        query: {
          mode,
          limit: 12,
          genres: [...genres].sort(),
          exclude_ids: pageParam,
        },
        signal,
      }).then(swipeCardsPageFromV2),
    initialPageParam: [] as string[],
    getNextPageParam: (lastPage, allPages) => {
      if (!lastPage.has_more || lastPage.paging_limited) return undefined;
      return [...new Set(allPages.flatMap((p) => p.cards.map((c) => c.content_id)))];
    },
    staleTime: 0,
    enabled,
  });
}
