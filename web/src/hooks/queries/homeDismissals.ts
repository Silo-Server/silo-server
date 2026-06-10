import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { api } from "@/api/client";
import {
  invalidateMediaSurfaceQueries,
  removeItemFromHomeSectionCaches,
} from "./mediaSurfaceRefresh";
import { bumpHomeRefreshSignal } from "@/pages/homeSurfaceRefresh";

export type HomeDismissalSurface = "continue_watching" | "next_up";

export interface DismissHomeItemVariables {
  itemId: string;
  surface: HomeDismissalSurface;
  mediaType?: string;
  seriesId?: string;
  progressUpdatedAt?: string;
}

function dismissalPath({ itemId, surface }: DismissHomeItemVariables) {
  return `/home/dismissals/${surface}/${encodeURIComponent(itemId)}`;
}

function dismissalBody({ progressUpdatedAt, seriesId, surface }: DismissHomeItemVariables) {
  return surface === "continue_watching"
    ? { progress_updated_at: progressUpdatedAt }
    : { series_id: seriesId };
}

function dismissalSuccessLabel({ mediaType, surface }: DismissHomeItemVariables) {
  if (surface === "next_up") return "Removed from Next Up";
  if (mediaType === "audiobook") return "Removed from Continue Listening";
  if (mediaType === "ebook") return "Removed from Continue Reading";
  return "Removed from Continue Watching";
}

export function useDismissHomeItem() {
  const queryClient = useQueryClient();

  const undoMutation = useMutation({
    mutationFn: (variables: DismissHomeItemVariables) =>
      api(dismissalPath(variables), {
        method: "DELETE",
      }),
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to undo removal");
    },
    onSuccess: async (_data, variables) => {
      await invalidateMediaSurfaceQueries(queryClient, { itemId: variables.itemId });
      bumpHomeRefreshSignal(queryClient);
    },
  });

  return useMutation({
    mutationFn: (variables: DismissHomeItemVariables) =>
      api(dismissalPath(variables), {
        method: "PUT",
        body: JSON.stringify(dismissalBody(variables)),
      }),
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to remove item");
    },
    onSuccess: async (_data, variables) => {
      removeItemFromHomeSectionCaches(queryClient, variables.itemId, variables.surface);
      await invalidateMediaSurfaceQueries(queryClient, { itemId: variables.itemId });
      bumpHomeRefreshSignal(queryClient);
      toast.success(dismissalSuccessLabel(variables), {
        action: {
          label: "Undo",
          onClick: () => undoMutation.mutate(variables),
        },
      });
    },
  });
}
