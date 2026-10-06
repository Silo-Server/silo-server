import { useMutation, useQueryClient } from "@tanstack/react-query";
import { v2 } from "@/api/v2/request";
import { toast } from "sonner";
import { invalidateMediaSurfaceQueries } from "./mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";
import type { HistoryRemovalTarget } from "@/lib/historyRemoval";

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
