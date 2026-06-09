import { keepPreviousData, useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { AudiobookGroupsResponse } from "@/api/types";
import { catalogKeys } from "./keys";

export type AudiobookGroupBy = "author" | "narrator" | "series";
export type AudiobookGroupSort = "name" | "count" | "duration";

const GROUPS_FETCH_LIMIT = 500;

export async function fetchAudiobookGroups(
  libraryId: number,
  groupBy: AudiobookGroupBy,
  sort: AudiobookGroupSort,
  options?: RequestInit,
): Promise<AudiobookGroupsResponse> {
  const params = new URLSearchParams({
    library_id: String(libraryId),
    group_by: groupBy,
    sort,
    limit: String(GROUPS_FETCH_LIMIT),
  });
  return api<AudiobookGroupsResponse>(`/catalog/audiobook-groups?${params.toString()}`, options);
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
