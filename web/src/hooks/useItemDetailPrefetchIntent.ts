import { useCallback } from "react";
import { usePrefetchCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useDwellPrefetch } from "@/hooks/useDwellPrefetch";

/**
 * Handlers for a card that opens an item page: they warm the item's detail
 * query on hover or keyboard intent (see `useDwellPrefetch`). With the detail
 * cached before the click, the item page renders on its first frame instead of
 * waiting for the request.
 */
export function useItemDetailPrefetchIntent(contentId: string, libraryId?: number) {
  const prefetchItemDetail = usePrefetchCatalogItemDetail(libraryId);
  const prefetch = useCallback(
    () => prefetchItemDetail(contentId),
    [prefetchItemDetail, contentId],
  );
  return useDwellPrefetch(prefetch);
}
