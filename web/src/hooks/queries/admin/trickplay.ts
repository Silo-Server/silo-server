import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { v2 } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";
import { adminKeys } from "../keys";

export type AdminTrickplayFile = components["schemas"]["AdminTrickplayFile"];
export type AdminTrickplayLibrary = components["schemas"]["AdminTrickplayLibrary"];

/**
 * Seek-preview progress of every library that generates previews. A server
 * without seek previews answers with an error, which callers treat as "no
 * libraries", so this never retries.
 */
export function useAdminTrickplayLibraries({ enabled = true }: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: adminKeys.trickplayLibraries(),
    queryFn: ({ signal }) =>
      v2("GET /api/v2/admin/trickplay/libraries", { signal }).then((body) => body.items),
    enabled,
    retry: false,
    refetchInterval: 60_000,
  });
}

/** The seek previews of each of an item's media files. */
export function useAdminItemTrickplay(itemId: string, { enabled = true } = {}) {
  return useQuery({
    queryKey: adminKeys.itemTrickplay(itemId),
    queryFn: ({ signal }) =>
      v2("GET /api/v2/admin/items/{id}/trickplay", { path: { id: itemId }, signal }).then(
        (body) => body.files,
      ),
    enabled: enabled && itemId !== "",
    retry: false,
    refetchInterval: (query) =>
      query.state.data?.some((file) => file.state === "pending" || file.state === "running")
        ? 5_000
        : false,
  });
}

/** Queues an item's seek previews to be made again ahead of the backlog. */
export function useRegenerateItemTrickplay() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    // Regeneration is non-retryable: replaying it after a 401 could make
    // the previews again once the first request's work finishes.
    mutationFn: (itemId: string) =>
      v2("POST /api/v2/admin/items/{id}/trickplay/regenerate", {
        path: { id: itemId },
        retryAuthentication: false,
      }),
    onSuccess: ({ requeued }, itemId) => {
      toast.success(
        requeued === 0
          ? "No seek previews were queued"
          : `Seek previews queued for ${requeued} ${requeued === 1 ? "file" : "files"}`,
      );
      void queryClient.invalidateQueries({ queryKey: adminKeys.itemTrickplay(itemId) });
      void queryClient.invalidateQueries({ queryKey: adminKeys.trickplayLibraries() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Could not queue seek previews");
    },
  });
}
