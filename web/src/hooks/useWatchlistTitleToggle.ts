import { useCallback, useState } from "react";
import type { RequestMediaResult } from "@/api/types";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useToggleWatchlistTitle } from "@/hooks/queries/watchlistTitles";
import { watchlistTitlesAvailable } from "@/lib/watchlistTitles";

type WatchlistToggleTarget = Pick<
  RequestMediaResult,
  "media_type" | "tmdb_id" | "title" | "in_watchlist" | "request"
>;

function titleKey(item: Pick<RequestMediaResult, "media_type" | "tmdb_id">): string {
  return `${item.media_type}-${item.tmdb_id}`;
}

/**
 * Adds TMDB titles to the watchlist from a list of cards, or removes them,
 * and tracks which cards have a change in flight (see useSubmitMediaRequest
 * for why each card waits on its own call). `enabled` is false until the
 * server reports it keeps watchlist entries for titles outside the library.
 */
export function useWatchlistTitleToggle() {
  const featureStatus = useRequestFeatureStatus();
  const { mutateAsync } = useToggleWatchlistTitle();
  const [pendingKeys, setPendingKeys] = useState<ReadonlySet<string>>(() => new Set());

  const toggle = useCallback(
    (item: WatchlistToggleTarget) => {
      const key = titleKey(item);
      setPendingKeys((prev) => new Set(prev).add(key));
      mutateAsync({
        mediaType: item.media_type,
        tmdbID: item.tmdb_id,
        title: item.title,
        inWatchlist: Boolean(item.in_watchlist),
        request: item.request,
      })
        // The mutation's own onError already reported the failure.
        .catch(() => {})
        .finally(() => {
          setPendingKeys((prev) => {
            if (!prev.has(key)) return prev;
            const next = new Set(prev);
            next.delete(key);
            return next;
          });
        });
    },
    [mutateAsync],
  );

  const isPending = useCallback(
    (item: Pick<RequestMediaResult, "media_type" | "tmdb_id">) => pendingKeys.has(titleKey(item)),
    [pendingKeys],
  );

  return {
    enabled: watchlistTitlesAvailable(featureStatus.data),
    toggle,
    isPending,
  };
}
