import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import type { FileMarkersResponse, MarkerEditAuditResponse, SetMarkersRequest } from "@/api/types";
import { adminKeys, itemKeys } from "@/hooks/queries/keys";

/** Loads the markers + provenance for a catalog item's primary file. */
export function useItemMarkers(itemId: string | undefined, options?: { enabled?: boolean }) {
  return useQuery({
    queryKey: itemKeys.markers(itemId ?? ""),
    queryFn: () => api<FileMarkersResponse>(`/markers/items/${encodeURIComponent(itemId ?? "")}`),
    enabled: Boolean(itemId) && (options?.enabled ?? true),
    staleTime: 30_000,
  });
}

/** Sets/clears manual markers on a catalog item's primary file. */
export function useSetItemMarkers(itemId: string | undefined) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: SetMarkersRequest) =>
      api<FileMarkersResponse>(`/markers/items/${encodeURIComponent(itemId ?? "")}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: (data) => {
      toast.success("Markers saved");
      if (itemId) {
        queryClient.setQueryData(itemKeys.markers(itemId), data);
        // Detail/watch responses embed markers, so refresh them too. Invalidate
        // by the item-scoped prefix (without the trailing libraryId/fileId
        // discriminators) so every cached variant for this item is refreshed.
        void queryClient.invalidateQueries({ queryKey: ["items", "detail", itemId] });
        void queryClient.invalidateQueries({ queryKey: ["items", "watchDetail", itemId] });
        void queryClient.invalidateQueries({ queryKey: adminKeys.markerItemHistory(itemId) });
        void queryClient.invalidateQueries({ queryKey: adminKeys.markerHistoryRoot() });
      }
    },
    onError: (error: unknown) => {
      toast.error(error instanceof Error ? error.message : "Failed to save markers");
    },
  });
}

export function useItemMarkerHistory(
  itemId: string | undefined,
  options?: { enabled?: boolean; limit?: number },
) {
  const limit = options?.limit ?? 25;
  const historyKey = itemId ? adminKeys.markerItemHistory(itemId) : adminKeys.markerItemHistory("");
  return useQuery({
    queryKey: [...historyKey, limit],
    queryFn: () =>
      api<MarkerEditAuditResponse>(
        `/admin/markers/items/${encodeURIComponent(itemId ?? "")}/history?limit=${limit}`,
      ).then((data) => data.history ?? []),
    enabled: Boolean(itemId) && (options?.enabled ?? true),
    staleTime: 30_000,
  });
}
