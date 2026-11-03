/**
 * The Requests Discover page's layout, for Admin → Requests → Discover Page.
 */
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import {
  getAdminDiscoverLayoutV2,
  listAdminWatchProvidersV2,
  previewAdminDiscoverRowV2,
  type DiscoverLayout,
  type DiscoverRowBody,
} from "@/api/v2/adminRequests";
import type { RequestMediaType } from "@/api/types";
import { useDebounce } from "@/hooks/useDebounce";
import { stableJson } from "@/lib/homeRows/stableJson";
import { tmdbImageURL } from "@/lib/mediaRequests";
import {
  PREVIEW_DEBOUNCE_MS,
  PREVIEW_ITEM_LIMIT,
  type PreviewState,
} from "../homeRows/useRowPreview";
import { adminKeys } from "../keys";
import { REQUESTS_STALE_TIME } from "../useRequests";

/** The Requests Discover page's layout; works while requests are turned off. */
export function useRequestDiscoverLayout(enabled = true) {
  // Only enabled rows are fetched, so a turned-off row stays pending.
  const pending = (layout: DiscoverLayout | undefined) =>
    layout?.rows.some((row) => row.enabled && row.source?.state === "pending") ?? false;
  return useQuery({
    queryKey: adminKeys.requestDiscoverLayout(),
    queryFn: getAdminDiscoverLayoutV2,
    staleTime: REQUESTS_STALE_TIME,
    enabled,
    // A saved list row fetches its list in the background; read again until
    // every enabled list has an answer.
    refetchInterval: (query) => (pending(query.state.data) ? 5_000 : false),
  });
}

/**
 * A live preview of a row as it stands in the row dialog, for the shared
 * preview strip; null shows none. Like Home rows, the row is debounced and the
 * last titles stay on screen while the next ones load.
 */
export function useDiscoverRowPreview(row: DiscoverRowBody | null): PreviewState {
  // A string, so a re-render with an equal row does not restart the wait.
  const current = row ? stableJson(row) : null;
  const settled = useDebounce(current, PREVIEW_DEBOUNCE_MS);
  const query = useQuery({
    queryKey: adminKeys.requestDiscoverPreview(settled),
    queryFn: () => previewAdminDiscoverRowV2(JSON.parse(settled!) as DiscoverRowBody),
    enabled: current !== null && settled === current,
    placeholderData: keepPreviousData,
    staleTime: REQUESTS_STALE_TIME,
    retry: false,
  });
  if (current === null) return { status: "off" };
  // A failed read shows as failed, not as the titles of an earlier row.
  if (query.isError) return { status: "error" };
  if (query.data)
    return {
      status: "ready",
      items: query.data.results.slice(0, PREVIEW_ITEM_LIMIT).map((result) => ({
        id: `${result.media_type}-${result.tmdb_id}`,
        title: result.title,
        posterUrl: tmdbImageURL(result.poster_path) ?? undefined,
      })),
      totalCount: query.data.total_results,
      refreshing: settled !== current || query.isFetching,
    };
  return { status: "loading" };
}

/** The streaming services TMDB knows in a region, for a filter row. */
export function useRequestWatchProviders(mediaType: RequestMediaType, region: string) {
  const valid = /^[A-Za-z]{2}$/.test(region);
  return useQuery({
    queryKey: adminKeys.requestWatchProviders(mediaType, region.toUpperCase()),
    queryFn: () => listAdminWatchProvidersV2(mediaType, region.toUpperCase()),
    enabled: valid,
    staleTime: 24 * 60 * 60 * 1000,
    retry: false,
  });
}
