import type { RowGroup } from "@/lib/homeRows/catalog";

/** The id of a group's tab in the picker rail. */
export function groupTabId(baseId: string, group: RowGroup) {
  return `${baseId}-tab-${group}`;
}

/** The id of a group's section in the card list, which its tab scrolls to. */
export function groupPanelId(baseId: string, group: RowGroup) {
  return `${baseId}-group-${group}`;
}
