import type { QueryClient } from "@tanstack/react-query";
import { adminKeys, libraryKeys } from "@/hooks/queries/keys";
import { invalidateMediaSurfaceQueries } from "@/hooks/queries/mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

export function invalidateCatalogState(
  queryClient: QueryClient,
  options: {
    itemId?: string;
    libraryId?: number;
    allowDashboardRefetch: boolean;
    includeLibraryLists?: boolean;
  },
) {
  const { itemId, libraryId, allowDashboardRefetch, includeLibraryLists = true } = options;
  void invalidateMediaSurfaceQueries(queryClient, { itemId, libraryId }).then(() => {
    bumpHomeRefreshSignal(queryClient);
  });
  if (includeLibraryLists) {
    void queryClient.invalidateQueries({
      queryKey: adminKeys.libraries(),
      refetchType: allowDashboardRefetch ? "active" : "none",
    });
    void queryClient.invalidateQueries({ queryKey: adminKeys.libraryMatchQueueStatuses() });
    void queryClient.invalidateQueries({ queryKey: libraryKeys.all });
  }
  void queryClient.invalidateQueries({
    queryKey: adminKeys.stats(),
    refetchType: allowDashboardRefetch ? "active" : "none",
  });
}
