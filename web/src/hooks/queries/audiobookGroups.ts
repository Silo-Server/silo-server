import { keepPreviousData, useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { AudiobookGroupsResponse } from "@/api/types";
import { catalogKeys } from "./keys";

export type AudiobookGroupBy = "author" | "narrator" | "series";
export type AudiobookGroupSort = "name" | "count" | "duration";

// Page until the reported total is reached so client-side filtering sees the
// complete list. The server caches the full grouped list per request, so a
// larger page is a cheap in-memory slice — a bigger page size means fewer
// sequential round-trips for large libraries (the dominant cold-load cost).
// GROUPS_MAX_PAGES bounds the worst case (pathological libraries).
const GROUPS_PAGE_SIZE = 2000;
const GROUPS_MAX_PAGES = 20;

export async function fetchAudiobookGroups(
  libraryId: number,
  groupBy: AudiobookGroupBy,
  sort: AudiobookGroupSort,
  options?: RequestInit,
): Promise<AudiobookGroupsResponse> {
  const groups: AudiobookGroupsResponse["groups"] = [];
  let total = 0;

  for (let page = 0; page < GROUPS_MAX_PAGES; page++) {
    const params = new URLSearchParams({
      library_id: String(libraryId),
      group_by: groupBy,
      sort,
      limit: String(GROUPS_PAGE_SIZE),
      offset: String(page * GROUPS_PAGE_SIZE),
    });
    const response = await api<AudiobookGroupsResponse>(
      `/catalog/audiobook-groups?${params.toString()}`,
      options,
    );
    groups.push(...response.groups);
    total = response.total;
    if (response.groups.length === 0 || groups.length >= total) {
      break;
    }
  }

  return { total, groups };
}

export function useAudiobookGroups(
  libraryId: number,
  groupBy: AudiobookGroupBy,
  sort: AudiobookGroupSort,
) {
  return useQuery({
    queryKey: catalogKeys.audiobookGroups(libraryId, groupBy, sort),
    queryFn: () => fetchAudiobookGroups(libraryId, groupBy, sort),
    enabled: libraryId > 0,
    staleTime: 60_000,
    placeholderData: keepPreviousData,
  });
}
