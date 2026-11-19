import type { DraftField } from "./draft";
import type { ArtworkSlot } from "./scope";
import { t, type MessageKey } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

// --- Rules (Smart) ----------------------------------------------------------

export function rulesCaption() {
  return t("web.collections.copy.rulesCaption");
}
/** Under a personal Smart collection's rules. */
export function personalRulesNote() {
  return t("web.collections.copy.personalRulesNote");
}
export function allMyLibraries() {
  return t("web.collections.copy.allMyLibrariesInSentence");
}
/** A personal list's libraries when none is ticked, as the library menu reads. */
export function allMyLibrariesLabel() {
  return t("web.collections.copy.allMyLibraries");
}
/** The meta line's last part on a Smart collection. */
export function smartUpdatesItself() {
  return t("web.collections.copy.smartUpdatesItself");
}
/** After "Rules not saved" in the save bar. */
export function previewShowsUnsaved() {
  return t("web.collections.copy.previewShowsUnsaved");
}

export function previewLive() {
  return t("web.collections.copy.previewLive");
}
export function previewEmpty() {
  return t("web.collections.copy.previewEmpty");
}
export function previewEmptyHelp() {
  return t("web.collections.copy.previewEmptyHelp");
}
export function previewFailed() {
  return t("web.collections.copy.previewFailed");
}

/** The preview of a Smart collection with no complete rule, which would match every title. */
export function previewNeedsRule() {
  return t("web.collections.copy.previewNeedsRule");
}
export function previewNeedsRuleHelp() {
  return t("web.collections.copy.previewNeedsRuleHelp");
}

export function previewMatches(total: number): string {
  return t("web.collections.copy.previewMatches", {
    count: total,
    total: total.toLocaleString(formatLocale()),
  });
}

/** The save bar's hint on a new Smart collection, once the preview has answered. */
export function smartCreateHint(total: number): string {
  if (total === 0) return t("web.collections.copy.smartCreateHintNone");
  return t("web.collections.copy.smartCreateHint", { matches: previewMatches(total) });
}

// --- Order ------------------------------------------------------------------

export function orderHelp() {
  return t("web.collections.copy.orderHelp");
}
export function noLimit() {
  return t("web.collections.copy.noLimit");
}

/** A smart collection's stored default sort, which wins over Order until cleared. */
export function storedSortLine(sortLabel: string): string {
  return t("web.collections.copy.storedSortLine", { sort: sortLabel });
}

// --- A saved Synced list ----------------------------------------------------

export function syncNow() {
  return t("web.collections.copy.syncNow");
}
export function syncingNow() {
  return t("web.collections.copy.syncingNow");
}
/** Why Sync now waits: it runs the saved list, not unsaved changes to it. */
export function saveBeforeSync() {
  return t("web.collections.copy.saveBeforeSync");
}
export function lastSync() {
  return t("web.collections.copy.lastSync");
}
export function nextSync() {
  return t("web.collections.copy.nextSync");
}
export function notInYourLibraries() {
  return t("web.collections.copy.notInYourLibraries");
}
export function notSyncedYet() {
  return t("web.collections.copy.notSyncedYet");
}
export function notScheduled() {
  return t("web.collections.copy.notScheduled");
}
export function notCountedYet() {
  return t("web.collections.copy.notCountedYet");
}
export function whySkipped() {
  return t("web.collections.copy.whySkipped");
}
export function changeLink() {
  return t("web.collections.copy.changeLink");
}
export function mdblistLinkLabel() {
  return t("web.collections.copy.mdblistLink");
}
export function tmdbListLinkLabel() {
  return t("web.collections.copy.tmdbListLink");
}
export function linkChangesAtNextSync() {
  return t("web.collections.copy.linkChangesAtNextSync");
}
export function tmdbListInvalid() {
  return t("web.collections.copy.tmdbListInvalid");
}
export function pickAChart() {
  return t("web.collections.copy.pickAChart");
}
export function chartSetWhenMade() {
  return t("web.collections.copy.chartSetWhenMade");
}
export function franchiseIdLabel() {
  return t("web.collections.copy.franchiseIdLabel");
}
export function findFranchiseId() {
  return t("web.collections.copy.findFranchiseId");
}
export function franchiseIdHelp() {
  return t("web.collections.copy.franchiseIdHelp");
}
export function franchiseIdMissing() {
  return t("web.collections.copy.franchiseIdMissing");
}
export function franchiseIdInvalid() {
  return t("web.collections.copy.franchiseIdInvalid");
}
export function discoverLocked() {
  return t("web.collections.copy.discoverLocked");
}
export function discoverStillEditable() {
  return t("web.collections.copy.discoverStillEditable");
}
export function personalScheduleLocked() {
  return t("web.collections.copy.personalScheduleLocked");
}
export function listDecides() {
  return t("web.collections.copy.listDecides");
}
export function listDecidesItems(): string[] {
  return [t("web.collections.copy.listDecidesTitles"), t("web.collections.copy.listDecidesOrder")];
}
export function youDecide() {
  return t("web.collections.copy.youDecide");
}

/** What the editor still controls on a list-backed collection. */
export function youDecideItems(scheduleEditable: boolean): string[] {
  return [
    t("web.collections.copy.youDecideLook"),
    scheduleEditable
      ? t("web.collections.copy.youDecideOrderAndSchedule")
      : t("web.collections.copy.youDecideOrder"),
    t("web.collections.copy.youDecideWhere"),
  ];
}

// --- Save bar ---------------------------------------------------------------

const DRAFT_FIELD_LABEL: Readonly<Record<DraftField, MessageKey>> = {
  name: "web.collections.copy.fieldName",
  description: "web.collections.copy.fieldDescription",
  libraryIds: "web.collections.copy.fieldLibraries",
  rules: "web.collections.copy.fieldRules",
  rawSortConfig: "web.collections.copy.fieldOrder",
  showOnly: "web.collections.copy.fieldShowOnly",
  visibility: "web.collections.copy.showOnTabLabel",
  shared: "web.collections.copy.showToOtherProfilesLabel",
  inLibraryTabs: "web.collections.copy.showOnTabLabel",
  list: "web.collections.copy.fieldList",
  limit: "web.collections.copy.fieldLimit",
  schedule: "web.collections.copy.fieldSchedule",
};

/** How the save bar and the conflict banner name a draft field. */
export function draftFieldLabel(field: DraftField): string {
  return t(DRAFT_FIELD_LABEL[field]);
}

export function artworkSlotLabel(slot: ArtworkSlot): string {
  return slot === "poster"
    ? t("web.collections.copy.slotPoster")
    : t("web.collections.copy.slotBackdrop");
}

// --- Look (artwork) ---------------------------------------------------------

/**
 * What a slot will show after Save: a chosen image not saved yet, the saved
 * one, the collage the server made of the collection's titles, no collage yet
 * (none of its titles has a poster, or the collage is still being made), or
 * nothing at all (a backdrop, or a personal collection's poster on a server
 * that makes no personal collages).
 */
export type ArtworkState = "none" | "awaiting-collage" | "collage" | "new" | "saved";

/** "Poster: a collage of its titles · Backdrop: none", for the closed Look card. */
export function lookSummary(slots: ReadonlyArray<[ArtworkSlot, ArtworkState]>): string {
  return slots
    .map(([slot, state]) => {
      const values = { state: state === "awaiting-collage" ? "awaitingCollage" : state };
      return slot === "poster"
        ? t("web.collections.copy.lookPoster", values)
        : t("web.collections.copy.lookBackdrop", values);
    })
    .join(" · ");
}

/** Under the poster tile while it shows the collage. */
export function posterIsCollage() {
  return t("web.collections.copy.posterIsCollage");
}
/** Under the poster tile while there's no image and no collage yet. */
export function posterAwaitsCollage() {
  return t("web.collections.copy.posterAwaitsCollage");
}
/** Under the poster tile of a collection that never gets a collage. */
export function noPoster() {
  return t("web.collections.copy.noPoster");
}
/** Under the backdrop tile: what the image is for. */
export function backdropCaption() {
  return t("web.collections.copy.backdropCaption");
}

export function notCreatedYet() {
  return t("web.collections.copy.notCreatedYet");
}
export function titlesAlreadySaved() {
  return t("web.collections.copy.titlesAlreadySaved");
}
export function saveFailed() {
  return t("web.collections.copy.saveFailed");
}
/** `saveFailed` followed by the server's reason. */
export function saveFailedWithError(error: string) {
  return t("web.collections.copy.saveFailedWithError", { error });
}

/** Why Create waits on a server collection with no library yet. */
export function pickLibrariesFirst() {
  return t("web.collections.copy.pickLibrariesFirst");
}

/** Why Save waits on a saved server collection with every library unticked. */
export function pickALibrary() {
  return t("web.collections.copy.pickALibrary");
}

export function nameItThenCreate() {
  return t("web.collections.copy.nameItThenCreate");
}

/** Why Create waits on a server Smart collection that shows in no library yet. */
export function pickWhereItShowsFirst() {
  return t("web.collections.copy.pickWhereItShowsFirst");
}
/** Why Create waits on a Smart collection with no complete rule. */
export function addARuleThenCreate() {
  return t("web.collections.copy.addRuleThenCreate");
}
/** Why Save waits on a saved Smart collection whose last rule was removed. */
export function addAtLeastOneRule() {
  return t("web.collections.copy.addAtLeastOneRule");
}
