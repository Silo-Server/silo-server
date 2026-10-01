import type { QueryClient } from "@tanstack/react-query";
import { catalogKeys, ratingKeys, recKeys, sectionKeys } from "./keys";

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

function startsWith(queryKey: readonly unknown[], prefix: readonly unknown[]) {
  return (
    prefix.length <= queryKey.length && prefix.every((part, index) => part === queryKey[index])
  );
}
