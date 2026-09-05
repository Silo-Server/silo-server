import type { ItemDetail } from "@/api/types";
import type { V2Body } from "@/api/v2/request";

/** One target of the v2 history removal command, as the contract declares it. */
export type HistoryRemovalTarget = V2Body<"POST /api/v2/history/remove">["targets"][number];
export type HistoryRemovalScope = NonNullable<HistoryRemovalTarget["scope"]>;

type HistoryRemovalMediaType = ItemDetail["type"];

export function historyRemovalScopeForMediaType(
  mediaType: HistoryRemovalMediaType,
): HistoryRemovalScope {
  return mediaType === "series" || mediaType === "season" ? "show" : "item";
}

export function buildHistoryRemovalTarget(
  contentId: string,
  mediaType: HistoryRemovalMediaType,
): HistoryRemovalTarget {
  return {
    content_id: contentId,
    scope: historyRemovalScopeForMediaType(mediaType),
  };
}

export function historyRemovalLabelForMediaType(mediaType: HistoryRemovalMediaType): string {
  return historyRemovalScopeForMediaType(mediaType) === "show"
    ? "Remove Show Watch Data"
    : "Remove Watch Data";
}

export function historyRemovalDialogTitle(
  targets: Array<Pick<HistoryRemovalTarget, "scope">>,
): string {
  if (targets.length > 1) {
    return "Remove selected watch data?";
  }
  return targets[0]?.scope === "show" ? "Remove show watch data?" : "Remove watch data?";
}

export function historyRemovalDialogDescription(
  targets: Array<Pick<HistoryRemovalTarget, "scope">>,
): string {
  if (targets.length > 1) {
    return `${targets.length} selected items will have their watch history, watched status, and resume progress cleared for this profile.`;
  }
  return targets[0]?.scope === "show"
    ? "This clears the show's watch history, watched episodes, and resume progress for this profile."
    : "This clears the item's watch history, watched status, and resume progress for this profile.";
}
