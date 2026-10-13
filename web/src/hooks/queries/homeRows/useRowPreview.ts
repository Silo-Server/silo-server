import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useDebounce } from "@/hooks/useDebounce";
import { pageParam } from "@/lib/homeRows/pages";
import type { PreviewItem } from "@/components/calm/usePeekLimiter";
import { fetchRowPreview } from "@/lib/homeRows/peek";
import type { RowDraft } from "@/lib/homeRows/rowDraft";
import { stableJson } from "@/lib/homeRows/stableJson";
import type { PageRef } from "@/lib/homeRows/types";

/** How many titles the step 2 and Edit row strip shows. */
export const PREVIEW_ITEM_LIMIT = 7;
const PREVIEW_DEBOUNCE_MS = 400;

export type PreviewState =
  | { status: "off" }
  | { status: "loading" }
  | { status: "error" }
  | {
      status: "ready";
      items: PreviewItem[];
      totalCount: number;
      /** The titles are for an earlier draft; the current one's are on the way. */
      refreshing: boolean;
    };

/**
 * A live preview of a draft row, from the admin preview route. The draft is
 * debounced so typing does not send a request per keystroke, and the last
 * result stays on screen while the next one loads.
 */
export function useRowPreview(
  draft: Pick<RowDraft, "sectionType" | "config">,
  page: PageRef,
  enabled: boolean,
): PreviewState {
  // A string, so a re-render with an equal draft does not restart the wait.
  const current = stableJson({ sectionType: draft.sectionType, config: draft.config });
  const settled = useDebounce(current, PREVIEW_DEBOUNCE_MS);
  const query = useQuery({
    queryKey: ["home-row-preview", pageParam(page), settled, PREVIEW_ITEM_LIMIT],
    queryFn: ({ signal }) =>
      fetchRowPreview(
        JSON.parse(settled) as Pick<RowDraft, "sectionType" | "config">,
        page,
        PREVIEW_ITEM_LIMIT,
        signal,
      ),
    // Only once the draft has stopped changing; the last result stays meanwhile.
    enabled: enabled && settled === current && draft.sectionType !== "",
    placeholderData: keepPreviousData,
    staleTime: 5 * 60 * 1000,
    retry: false,
  });
  if (!enabled) return { status: "off" };
  if (query.data)
    return {
      status: "ready",
      ...query.data,
      refreshing: settled !== current || query.isFetching,
    };
  if (query.isError) return { status: "error" };
  return { status: "loading" };
}
