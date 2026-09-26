import { useMutation, useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { catalogItemFromV2 } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { toast } from "sonner";
import { historyKeys } from "./keys";
import { invalidateMediaSurfaceQueries } from "./mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";
import type { HistoryRemovalTarget } from "@/lib/historyRemoval";

export function useHistory() {
  return useInfiniteQuery({
    queryKey: historyKeys.list(),
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam, signal }) => {
      const page = await v2("GET /api/v2/history", {
        query: pageParam ? { cursor: pageParam } : undefined,
        signal,
      });
      return { ...page, items: page.items.map(catalogItemFromV2) };
    },
    // Empty raw windows can still precede unseen cards. Load another page
    // only when requested, and use its cursor rather than its item count.
    getNextPageParam: (lastPage) => lastPage.page?.next_cursor || undefined,
  });
}

export function useRemoveHistory() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (targets: HistoryRemovalTarget[]) =>
      v2("POST /api/v2/history/remove", { body: { targets } }),
    onSuccess: async (_data, targets) => {
      toast.success(
        targets.length === 1
          ? "Removed watch data"
          : `Removed watch data for ${targets.length} items`,
      );
      await invalidateMediaSurfaceQueries(queryClient);
      bumpHomeRefreshSignal(queryClient);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove history");
    },
  });
}
