import { t } from "@/i18n";

// --- Arrange ----------------------------------------------------------------

export function arrangeHeading(libraryName: string): string {
  return t("web.collections.copy.arrangeHeading", { library: libraryName });
}
export function arrangeSubtitle() {
  return t("web.collections.copy.arrangeSubtitle");
}
export function arrangeHint() {
  return t("web.collections.copy.arrangeHint");
}

export function noHeading() {
  return t("web.collections.copy.noHeading");
}
export function noHeadingHelp() {
  return t("web.collections.copy.noHeadingHelp");
}
export function myCollectionsTag() {
  return t("web.collections.copy.myCollectionsTag");
}
export function myCollectionsNote() {
  return t("web.collections.copy.myCollectionsNote");
}
/** Shown on My collections while a server collection is dragged. */
export function myCollectionsNoDrop() {
  return t("web.collections.copy.myCollectionsNoDrop");
}
export function hiddenTag() {
  return t("web.collections.copy.hiddenTag");
}
export function moveFailedMessage() {
  return t("web.collections.copy.moveFailed");
}
/** A move found the order changed by someone else since Arrange read it; nothing was saved. */
export function orderChanged() {
  return t("web.collections.copy.orderChanged");
}

export function viewerPreviewLabel() {
  return t("web.collections.copy.viewerPreviewLabel");
}
export function viewerPreviewMine() {
  return t("web.collections.copy.viewerPreviewMine");
}
export function viewerPreviewNote() {
  return t("web.collections.copy.viewerPreviewNote");
}

// --- Pin (`featured`) ---------------------------------------------------------

export function pinLabel() {
  return t("web.collections.copy.pinLabel");
}
export function unpinLabel() {
  return t("web.collections.copy.unpinLabel");
}
export function pinnedTag() {
  return t("web.collections.copy.pinned");
}
export function pinnedBandLabel() {
  return t("web.collections.copy.pinnedBand");
}

/** Pin is set on the collection, not per library, so it reaches every library the collection is in. */
function withEveryLibrary(help: string, inSeveralLibraries: boolean): string {
  return inSeveralLibraries ? `${help} ${t("web.collections.copy.pinEveryLibrary")}` : help;
}

/**
 * What Pin does, given what the shelf sorts by (null for Your order) and
 * whether the collection is in more than one library. Pinned collections also
 * lead their library's collections in the Server collections list on every
 * profile's Collections page, which is all Pin does on a shelf that sorts
 * itself.
 */
export function pinHelp(shelfSortedBy: string | null, inSeveralLibraries = false): string {
  const help =
    shelfSortedBy === null
      ? t("web.collections.copy.pinHelp")
      : t("web.collections.copy.pinHelpSorted", { sort: shelfSortedBy });
  return withEveryLibrary(help, inSeveralLibraries);
}

export function unpinHelp(shelfSortedBy: string | null, inSeveralLibraries = false): string {
  const help =
    shelfSortedBy === null
      ? t("web.collections.copy.unpinHelp")
      : t("web.collections.copy.unpinHelpSorted");
  return withEveryLibrary(help, inSeveralLibraries);
}

/**
 * The phone sheet's Pin switch, which names the collection and its shelf
 * (null for No heading, which viewers never see as a name).
 */
export function pinSwitchLabel(name: string, shelfName: string | null): string {
  return shelfName === null
    ? t("web.collections.copy.pinSwitchNoHeading", { name })
    : t("web.collections.copy.pinSwitch", { name, shelf: shelfName });
}

export function deleteShelfTitle(name: string): string {
  return t("web.collections.copy.deleteShelfTitle", { name });
}
