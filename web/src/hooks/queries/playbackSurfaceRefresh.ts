import type { QueryClient } from "@tanstack/react-query";
import { isLibraryLayoutQueryKey } from "@/lib/queryInvalidation";
import { catalogKeys, historyKeys, personKeys, progressKeys, recKeys, sectionKeys } from "./keys";

/**
 * Marks playback-derived surfaces stale so the next mounted screen revalidates
 * continue-watching and related rows without a full app refresh.
 */
export async function invalidatePlaybackSurfaceQueries(queryClient: QueryClient) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: catalogKeys.all }),
    queryClient.invalidateQueries({ queryKey: progressKeys.all }),
    queryClient.invalidateQueries({ queryKey: historyKeys.all }),
    queryClient.invalidateQueries({ queryKey: recKeys.all }),
    queryClient.invalidateQueries({ queryKey: personKeys.all }),
    queryClient.invalidateQueries({ queryKey: sectionKeys.homeItemsRoot() }),
    queryClient.invalidateQueries({
      queryKey: sectionKeys.libraryRoot(),
      predicate: (query) => !isLibraryLayoutQueryKey(query.queryKey),
    }),
  ]);
}
