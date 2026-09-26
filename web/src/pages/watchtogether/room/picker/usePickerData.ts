import { useMemo } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { fetchCatalogPage, createCatalogSearchState } from "@/hooks/queries/catalog";
import {
  fetchHomeSectionItems,
  HOME_SECTION_GC_TIME,
  HOME_SECTION_STALE_TIME,
  useHomeLayout,
} from "@/hooks/queries/sections";
import { sectionKeys } from "@/hooks/queries/keys";
import type { BrowseItem, SectionItem } from "@/api/types";
import {
  getWatchTogetherRoomPicker,
  queryWatchTogetherMemberState,
  type WatchTogetherRoomMember,
} from "@/lib/watchTogether";
import type { ItemMemberState } from "@/api/v2/watchTogetherMemberState";
import { useDebounce } from "@/hooks/useDebounce";

export const pickerKeys = {
  rows: (roomId: string, members: WatchTogetherRoomMember[]) =>
    [
      "watch-party",
      "picker",
      roomId,
      members.map((m) => `${m.user_id}:${m.profile_id}`).sort(),
    ] as const,
  memberState: (roomId: string, members: WatchTogetherRoomMember[], ids: string[]) =>
    [
      "watch-party",
      "member-state",
      roomId,
      members.map((m) => `${m.user_id}:${m.profile_id}`).sort(),
      ids,
    ] as const,
  search: (q: string) => ["watch-party", "search", q] as const,
  recent: (scope: "movie" | "series") => ["watch-party", "recently-added", scope] as const,
};

export type ShelfItem = Pick<
  BrowseItem,
  "content_id" | "type" | "title" | "year" | "poster_url" | "poster_thumbhash"
>;

function isPickable<T extends { type: string }>(item: T): item is T & { type: "movie" | "series" } {
  return item.type === "movie" || item.type === "series";
}

/** The server-computed together rows. Refetched when the member set changes. */
export function usePickerRows(
  roomId: string | undefined,
  roomToken: string | null,
  members: WatchTogetherRoomMember[],
) {
  return useQuery({
    queryKey: pickerKeys.rows(roomId ?? "", members),
    queryFn: () => getWatchTogetherRoomPicker(roomId!, roomToken!),
    enabled: !!roomId && !!roomToken,
    staleTime: 30_000,
  });
}

/**
 * Member watch state for a set of content ids, keyed by a sorted id set so
 * the same episodes list does not refetch when order or duplicates change.
 * Reads are chunked at the server bound.
 */
export function useMemberState(
  roomId: string | undefined,
  roomToken: string | null,
  members: WatchTogetherRoomMember[],
  contentIds: string[],
) {
  const ids = useMemo(
    () => Array.from(new Set(contentIds.filter(Boolean))).sort(),
    // A new array each render is fine: the key is by value.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [contentIds.join("\u0000")],
  );
  const query = useQuery({
    queryKey: pickerKeys.memberState(roomId ?? "", members, ids),
    queryFn: async () => {
      const items: ItemMemberState[] = [];
      for (let i = 0; i < ids.length; i += 200) {
        const page = await queryWatchTogetherMemberState(
          roomId!,
          roomToken!,
          ids.slice(i, i + 200),
        );
        items.push(...page.items);
      }
      return items;
    },
    enabled: !!roomId && !!roomToken && ids.length > 0,
    staleTime: 30_000,
  });
  const byId = useMemo(() => {
    const map = new Map<string, ItemMemberState>();
    for (const item of query.data ?? []) map.set(item.content_id, item);
    return map;
  }, [query.data]);
  return { ...query, byId };
}

/** Catalog search over movies and series, debounced. */
export function usePickerSearch(rawQuery: string) {
  const q = useDebounce(rawQuery.trim(), 200);
  const state = useMemo(() => createCatalogSearchState("query", q ? { q } : {}), [q]);
  const query = useQuery({
    queryKey: pickerKeys.search(q),
    queryFn: ({ signal }) => fetchCatalogPage(state, 30, 0, { signal }),
    enabled: q.length > 0,
    staleTime: 60_000,
  });
  const items = useMemo(
    () =>
      (query.data?.items ?? []).filter((item) => item.type === "movie" || item.type === "series"),
    [query.data?.items],
  );
  return { q, items, isFetching: query.isFetching, isError: query.isError, refetch: query.refetch };
}

/** The newest additions of one kind, so a movie-heavy week cannot crowd series out. */
export function useRecentlyAdded(enabled: boolean, scope: "movie" | "series") {
  const state = useMemo(
    () =>
      createCatalogSearchState("query", {
        explicit_sort: true,
        query_definition: {
          library_ids: [],
          match: "all",
          groups: [],
          media_scope: scope,
          sort: { field: "added_at", order: "desc" },
        },
      }),
    [scope],
  );
  const query = useQuery({
    queryKey: pickerKeys.recent(scope),
    queryFn: ({ signal }) => fetchCatalogPage(state, 24, 0, { signal }, false),
    enabled,
    staleTime: 120_000,
  });
  const items = useMemo(() => (query.data?.items ?? []).filter(isPickable), [query.data?.items]);
  return { items, isFetching: query.isFetching };
}

/**
 * Home rows the shelf skips: personal rows (continue watching, next up,
 * watchlist…) are covered by the together rows or would leak one member's
 * history into the shared shelf, and the per-library "recently added" rows
 * are replaced by the shelf's own movie and series rows.
 */
const skippedSectionTypes = new Set([
  "recently_added",
  "recently_released",
  "new_to_library",
  "continue_watching",
  "next_up",
  "next_in_series",
  "watchlist",
  "favorites",
  "profile_activity_feed",
  "because_you_watched",
  "recommended_for_you",
  "similar_users_liked",
  "taste_match",
  "forgotten_favorites",
]);
const maxShelfSections = 4;

export interface ShelfSection {
  id: string;
  title: string;
  items: (SectionItem & { type: "movie" | "series" })[];
}

/**
 * The first few discovery rows from the home screen (trending, curated
 * collections, hidden gems…) with only movies and series kept. Shares the
 * home page's section cache so a room opened from home costs nothing extra.
 */
export function useHomeShelfSections(enabled: boolean) {
  const layout = useHomeLayout(enabled);
  const wanted = useMemo(
    () =>
      (layout.data?.sections ?? [])
        .filter((section) => !skippedSectionTypes.has(section.section_type))
        .slice(0, maxShelfSections),
    [layout.data?.sections],
  );
  const results = useQueries({
    queries: wanted.map((section) => ({
      queryKey: sectionKeys.homeItems(section.id),
      queryFn: ({ signal }: { signal?: AbortSignal }) =>
        fetchHomeSectionItems(section.id, { signal }),
      staleTime: HOME_SECTION_STALE_TIME,
      gcTime: HOME_SECTION_GC_TIME,
      enabled,
    })),
  });
  const sections = useMemo<ShelfSection[]>(
    () =>
      wanted.flatMap((section, index) => {
        const resolved = results[index]?.data?.section;
        if (!resolved) return [];
        const items = resolved.items.filter(isPickable);
        return items.length > 0 ? [{ id: section.id, title: resolved.title, items }] : [];
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- results is a new array each render; its data is what matters
    [wanted, ...results.map((result) => result.data)],
  );
  const isFetching = layout.isFetching || results.some((result) => result.isFetching);
  return { sections, isFetching };
}
