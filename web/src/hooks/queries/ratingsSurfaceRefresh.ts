import type { QueryClient } from "@tanstack/react-query";
import { adminKeys, catalogKeys, ratingKeys, recKeys, sectionKeys } from "./keys";

export async function invalidateRatingSurfaceQueries(queryClient: QueryClient, itemId: string) {
  // No `cancelRefetch: false` here: reusing an in-flight request would let a
  // response that predates the rating change satisfy this invalidation and land
  // in the cache as fresh.
  await queryClient.invalidateQueries({
    predicate: (query) => {
      const key = query.queryKey;
      const isCurrentDetail =
        key[0] === "catalog" && key[1] === "items" && key[2] === itemId && key[3] === "detail";
      if (isCurrentDetail) return false;
      const isSimilarItems = startsWith(key, recKeys.all) && key[1] === "similar";
      if (isSimilarItems) return false;

      return (
        startsWith(key, ratingKeys.item(itemId)) ||
        startsWith(key, catalogKeys.all) ||
        startsWith(key, recKeys.all) ||
        startsWith(key, sectionKeys.all)
      );
    },
  });
}

// invalidateAllRatingSurfaceQueries marks every rating-derived surface stale
// after ratings changed outside a single-item edit, such as a watch-provider
// import.
export async function invalidateAllRatingSurfaceQueries(queryClient: QueryClient) {
  await queryClient.invalidateQueries({
    predicate: (query) =>
      startsWith(query.queryKey, ratingKeys.all) ||
      startsWith(query.queryKey, catalogKeys.all) ||
      startsWith(query.queryKey, recKeys.all) ||
      startsWith(query.queryKey, sectionKeys.all),
  });
}

/**
 * How long after a change to the rating choice the surfaces refresh a second
 * time: each API node keeps its copy of the choice for up to 10 seconds.
 */
export const RATING_POLICY_SETTLE_MS = 11_000;

/**
 * Refreshes every rating surface and the admin list of rating sources after
 * the rating choice may have changed (the setting, or a plugin that declares
 * ratings), now and again once each API node's cached copy has expired.
 */
export function refreshRatingChoice(
  queryClient: QueryClient,
  schedule: (run: () => void, ms: number) => unknown = (run, ms) => setTimeout(run, ms),
) {
  const refresh = () => {
    void invalidateAllRatingSurfaceQueries(queryClient);
    void queryClient.invalidateQueries({ queryKey: adminKeys.ratingSources() });
  };
  refresh();
  schedule(refresh, RATING_POLICY_SETTLE_MS);
}

function startsWith(queryKey: readonly unknown[], prefix: readonly unknown[]) {
  return (
    prefix.length <= queryKey.length && prefix.every((part, index) => part === queryKey[index])
  );
}
