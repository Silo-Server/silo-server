import { useQueries } from "@tanstack/react-query";

import { createLimiter, PEEK_MAX_IN_FLIGHT } from "@/components/calm/usePeekLimiter";
import { profileCollectionRows, type RowsState } from "@/lib/collections/rows";
import type { PageRef } from "@/lib/homeRows/types";
import { fetchProfileSectionSettings, profileSectionSettingsKey } from "./sections";

/** Page reads for Where it shows: never more than the peeks' budget in flight at once. */
const runPageRead = createLimiter(PEEK_MAX_IN_FLIGHT);

const NO_PAGES: readonly PageRef[] = [];

/**
 * The rows on this profile's own `pages` that show personal collection
 * `collectionId`. Each page is read as Settings > Home Screen reads it (the
 * same cache, so a row added there is here on the way back), at most
 * `PEEK_MAX_IN_FLIGHT` pages at once. `pages` is undefined while the pages
 * aren't known; with no `collectionId` (not created yet) nothing is read.
 */
export function useProfileCollectionRows(
  collectionId: string | undefined,
  pages: readonly PageRef[] | undefined,
): RowsState {
  const results = useQueries({
    queries: (pages ?? NO_PAGES).map((page) => {
      const libraryId = page.kind === "library" ? page.libraryId : undefined;
      return {
        queryKey: profileSectionSettingsKey(page.kind, libraryId),
        queryFn: ({ signal }: { signal: AbortSignal }) =>
          runPageRead(() => fetchProfileSectionSettings(page.kind, libraryId, signal), signal),
        enabled: Boolean(collectionId),
      };
    }),
  });

  if (!collectionId) return { status: "ready", rows: [] };
  if (!pages) return { status: "loading" };
  if (results.some((result) => result.isError)) {
    return {
      status: "error",
      onRetry: () => {
        for (const result of results) if (result.isError) void result.refetch();
      },
    };
  }
  if (results.some((result) => !result.data)) return { status: "loading" };
  return {
    status: "ready",
    rows: profileCollectionRows(
      collectionId,
      pages.map((page, index) => ({ page, sections: results[index]!.data!.sections })),
    ),
  };
}
