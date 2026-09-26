import { useInfiniteQuery } from "@tanstack/react-query";

import type { AudiobookGroup } from "@/api/types";
import { v2 } from "@/api/v2/request";
import { catalogKeys } from "./keys";

export type AudiobookGroupBy = "author" | "narrator" | "series";
export type AudiobookGroupSort = "name" | "count" | "duration";

const GROUPS_PAGE_SIZE = 60;

/** One page of grouped audiobooks; `next_cursor` is absent on the last page. */
export interface AudiobookGroupsPage {
  total: number;
  total_exact: boolean;
  has_more: boolean;
  next_cursor?: string;
  groups: AudiobookGroup[];
}

export async function fetchAudiobookGroupsPage(
  libraryId: number,
  groupBy: AudiobookGroupBy,
  sort: AudiobookGroupSort,
  cursor: string,
  includeTotal: boolean,
  searchPrefix: string,
  options?: Pick<RequestInit, "signal">,
): Promise<AudiobookGroupsPage> {
  const trimmedSearch = searchPrefix.trim();
  const page = await v2("GET /api/v2/catalog/audiobook-groups", {
    query: {
      library_id: String(libraryId),
      group_by: groupBy,
      sort,
      limit: GROUPS_PAGE_SIZE,
      cursor: cursor || undefined,
      skip_total: includeTotal ? undefined : true,
      q: trimmedSearch || undefined,
    },
    signal: options?.signal ?? undefined,
  });
  return {
    total: page.total,
    total_exact: page.total_exact,
    has_more: page.page?.has_more ?? false,
    next_cursor: page.page?.next_cursor,
    groups: page.items,
  };
}

export function useAudiobookGroups(
  libraryId: number,
  groupBy: AudiobookGroupBy,
  sort: AudiobookGroupSort,
  searchPrefix = "",
) {
  const search = searchPrefix.trim();
  return useInfiniteQuery({
    queryKey: catalogKeys.audiobookGroups(libraryId, groupBy, sort, search),
    queryFn: ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) =>
      fetchAudiobookGroupsPage(libraryId, groupBy, sort, pageParam, pageParam === "", search, {
        signal,
      }),
    initialPageParam: "",
    getNextPageParam: (lastPage) =>
      lastPage.has_more && lastPage.next_cursor ? lastPage.next_cursor : undefined,
    enabled: libraryId > 0,
    staleTime: 60_000,
  });
}
