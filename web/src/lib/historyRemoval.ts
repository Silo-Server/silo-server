import type { ItemDetail } from "@/api/types";
import type { V2Body } from "@/api/v2/request";
import { t } from "@/i18n";

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
    return t("web.catalog.history.dialogTitleSelected");
  }
  return targets[0]?.scope === "show"
    ? t("web.catalog.history.dialogTitleShow")
    : t("web.catalog.history.dialogTitle");
}

export function historyRemovalDialogDescription(
  targets: Array<Pick<HistoryRemovalTarget, "scope">>,
): string {
  if (targets.length > 1) {
    return t("web.catalog.history.dialogBodySelected", {
      count: targets.length,
      selected: targets.length,
    });
  }
  return targets[0]?.scope === "show"
    ? t("web.catalog.history.dialogBodyShow")
    : t("web.catalog.history.dialogBody");
}
