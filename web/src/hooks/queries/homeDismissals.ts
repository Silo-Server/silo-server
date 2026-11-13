import { useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { t } from "@/i18n";
import { v2 } from "@/api/v2/request";
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

/** A card's removal, when its row lets it be removed from Home. */
export function homeDismissalForCard(card: {
  content_id: string;
  type: string;
  series_id?: string;
  item_source?: string;
  progress_updated_at?: string;
}): DismissHomeItemVariables | undefined {
  if (card.item_source === "next_up") {
    return card.series_id
      ? {
          itemId: card.content_id,
          surface: "next_up",
          mediaType: card.type,
          seriesId: card.series_id,
        }
      : undefined;
  }
  return card.progress_updated_at
    ? {
        itemId: card.content_id,
        surface: "continue_watching",
        mediaType: card.type,
        progressUpdatedAt: card.progress_updated_at,
      }
    : undefined;
}

function dismissalPath({ itemId, surface }: DismissHomeItemVariables) {
  return { surface, item_id: itemId };
}

function dismissalBody({ progressUpdatedAt, seriesId, surface }: DismissHomeItemVariables) {
  return surface === "continue_watching"
    ? { progress_updated_at: progressUpdatedAt }
    : { series_id: seriesId };
}

function dismissalSuccessLabel({ mediaType, surface }: DismissHomeItemVariables) {
  // Dismissing an episode or series hides the whole show from both rows.
  if (mediaType === "episode" || mediaType === "series") {
    return t("web.home.dismiss.removedContinueWatchingAndNextUp");
  }
  if (surface === "next_up") return t("web.home.dismiss.removedNextUp");
  if (mediaType === "audiobook") return t("web.home.dismiss.removedContinueListening");
  if (mediaType === "ebook") return t("web.home.dismiss.removedContinueReading");
  return t("web.home.dismiss.removedContinueWatching");
}

export function useDismissHomeItem() {
  const queryClient = useQueryClient();

  const undoMutation = useMutation({
    mutationFn: (variables: DismissHomeItemVariables) =>
      v2("DELETE /api/v2/home/dismissals/{surface}/{item_id}", { path: dismissalPath(variables) }),
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t("web.home.dismiss.undoFailed"));
    },
    onSuccess: async (_data, variables) => {
      await invalidateMediaSurfaceQueries(queryClient, { itemId: variables.itemId });
      bumpHomeRefreshSignal(queryClient);
    },
  });

  return useMutation({
    mutationFn: (variables: DismissHomeItemVariables) =>
      v2("PUT /api/v2/home/dismissals/{surface}/{item_id}", {
        path: dismissalPath(variables),
        body: dismissalBody(variables),
      }),
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : t("web.home.dismiss.removeFailed"));
    },
    onSuccess: async (_data, variables) => {
      removeItemFromHomeSectionCaches(queryClient, variables.itemId, variables.surface);
      await invalidateMediaSurfaceQueries(queryClient, { itemId: variables.itemId });
      bumpHomeRefreshSignal(queryClient);
      toast.success(dismissalSuccessLabel(variables), {
        action: {
          label: t("web.home.dismiss.undo"),
          onClick: () => undoMutation.mutate(variables),
        },
      });
    },
  });
}
