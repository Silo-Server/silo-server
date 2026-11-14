import type { ScopeKind } from "./scope";
import type { CollectionKind } from "./types";
import { t, type MessageKey } from "@/i18n";
import { appLanguage, formatLocale } from "@/lib/appLocale";
import { titlesAlreadySaved } from "./copyEditor";

export * from "./copyEditor";
export * from "./copyArrange";

/**
 * Words every collections surface shares, so the editor, lists and dialogs
 * say the same thing. "Collections tab" is where a library's collections are
 * browsed; "library page" is the rows above a library's grid.
 *
 * Everything here is a function: it reads the interface language when called,
 * so call it while rendering, never at module scope.
 */

/** "Movies", "Movies and Kids", "Movies, Kids and 4K Movies", in the interface language. */
export function joinNames(names: readonly string[]): string {
  if (names.length <= 1) return names[0] ?? "";
  const language = appLanguage();
  let format = listFormats.get(language);
  if (!format) {
    // English keeps its list without the serial comma, as en-GB writes it.
    format = new Intl.ListFormat(language === "en" ? "en-GB" : language, {
      style: "long",
      type: "conjunction",
    });
    listFormats.set(language, format);
  }
  return format.format(names);
}

const listFormats = new Map<string, Intl.ListFormat>();

/** "Movies › Collections": a library's Collections tab. */
function collectionsTabOf(libraryName: string): string {
  return t("web.collections.copy.collectionsTabOf", { library: libraryName });
}

// --- New collection ---------------------------------------------------------

export function newCollection() {
  return t("web.collections.copy.newCollection");
}

/** The type picker's subtitle: what the type decides, and what the editor asks next. */
export function newCollectionHelp(scope: ScopeKind): string {
  return scope === "server"
    ? t("web.collections.copy.newCollectionHelpServer")
    : t("web.collections.copy.newCollectionHelpPersonal");
}

export function newCollectionNext() {
  return t("web.collections.copy.newCollectionNext");
}
export function starterPackPrompt() {
  return t("web.collections.copy.starterPackPrompt");
}
export function addAStarterPack() {
  return t("web.collections.copy.addAStarterPack");
}

const KIND_SENTENCE: Readonly<Record<CollectionKind, MessageKey>> = {
  manual: "web.collections.copy.kindSentenceManual",
  smart: "web.collections.copy.kindSentenceSmart",
  synced: "web.collections.copy.kindSentenceSynced",
};

/** What each type does, under its name on the type picker. */
export function kindSentence(kind: CollectionKind): string {
  return t(KIND_SENTENCE[kind]);
}

/** Examples for each type on the type picker; a profile has no staff. */
export function kindGoodFor(scope: ScopeKind, kind: CollectionKind): string {
  switch (kind) {
    case "manual":
      return scope === "server"
        ? t("web.collections.copy.goodForManualServer")
        : t("web.collections.copy.goodForManualPersonal");
    case "smart":
      return t("web.collections.copy.goodForSmart");
    case "synced":
      return t("web.collections.copy.goodForSynced");
  }
}

export function syncedChecking() {
  return t("web.collections.copy.syncedChecking");
}
export function syncedCheckFailed() {
  return t("web.collections.copy.syncedCheckFailed");
}

// --- Where it shows ---------------------------------------------------------

export function showOnTabLabel() {
  return t("web.collections.copy.showOnTabLabel");
}

/** The server switch's help: where viewers find the collection. */
export function serverTabHelp(libraryNames: readonly string[]): string {
  if (libraryNames.length === 0) return t("web.collections.copy.serverTabHelpNone");
  return t("web.collections.copy.serverTabHelp", {
    places: joinNames(libraryNames.map(collectionsTabOf)),
  });
}

/**
 * The personal switch's help. Profiles the collection is shared with see it
 * on their Collections tabs too, so the label can't say "my".
 */
export function personalTabHelp() {
  return t("web.collections.copy.personalTabHelp");
}

export function showToOtherProfilesLabel() {
  return t("web.collections.copy.showToOtherProfilesLabel");
}
export function showToOtherProfilesHelp() {
  return t("web.collections.copy.showToOtherProfilesHelp");
}

/** What turning sharing off costs: "Maya and Leo lose it, including rows they made from it." */
export function unshareConsequence(profileNames: readonly string[]): string {
  if (profileNames.length === 0) return t("web.collections.copy.unshareConsequenceOthers");
  return t("web.collections.copy.unshareConsequence", {
    count: profileNames.length,
    names: joinNames(profileNames),
  });
}

/** Shown when sharing is turned off on a saved collection. */
export function unshareWarning(profileNames: readonly string[]): string {
  return t("web.collections.copy.unshareWarning", {
    consequence: unshareConsequence(profileNames),
  });
}

/** The ⋯ switch's help on a card: the menu has room for one short line. */
export function showToOtherProfilesShortHelp() {
  return t("web.collections.copy.showToOtherProfilesShortHelp");
}

/** The Libraries row while a server collection has none ticked. */
export function librariesNotPicked() {
  return t("web.collections.copy.librariesNotPicked");
}
/** A server Smart collection's Libraries row: where it shows, not where its titles come from. */
export function smartLibrariesHelp() {
  return t("web.collections.copy.smartLibrariesHelp");
}
/** The Shelf row of a collection that isn't created yet, after "No heading". */
export function shelfAfterCreate() {
  return t("web.collections.copy.shelfAfterCreate");
}

// --- Server list ------------------------------------------------------------

export function onHome() {
  return t("web.collections.copy.onHome");
}
export function hiddenFromTab() {
  return t("web.collections.copy.hiddenFromTab");
}

/** "Movies › Collections and Kids › Collections". */
function collectionsTabs(libraryNames: readonly string[]): string {
  return joinNames(libraryNames.map(collectionsTabOf));
}

export function hideCollectionTitle(name: string): string {
  return t("web.collections.copy.hideCollectionTitle", { name });
}

/**
 * Hiding a collection rows show: they keep showing it, but See all can't open
 * it. `places` names where the rows are ("Home and the Kids page") when known.
 */
export function hideCollectionDescription(
  libraryNames: readonly string[],
  rowCount: number,
  places?: string | null,
): string {
  const rows = places
    ? t("web.collections.copy.hideCollectionRowsOn", {
        count: rowCount,
        count_text: rowCount,
        places,
      })
    : t("web.collections.copy.hideCollectionRows", {
        count: rowCount,
        count_text: rowCount,
      });
  if (libraryNames.length === 0) return rows;
  return `${t("web.collections.copy.hideCollectionLeaves", { tabs: collectionsTabs(libraryNames) })} ${rows}`;
}

/** A delete the server refused because Home or library page rows still show the collection. */
export function collectionInUse() {
  return t("web.collections.copy.collectionInUse");
}

// --- Where it shows: rows ---------------------------------------------------

export function rowsThatShowIt() {
  return t("web.collections.copy.rowsThatShowIt");
}
export function rowsNotListed() {
  return t("web.collections.copy.rowsNotListed");
}
export function noRowsYet() {
  return t("web.collections.copy.noRowsYet");
}
export function rowsFailed() {
  return t("web.collections.copy.rowsFailed");
}
export function addAsARow() {
  return t("web.collections.copy.addAsARow");
}
export function otherLibraries() {
  return t("web.collections.copy.otherLibraries");
}
/** The Rows that show it row of a collection that isn't created yet. */
export function rowsOnceCreated() {
  return t("web.collections.copy.rowsOnceCreated");
}
export function showItFirst() {
  return t("web.collections.copy.showItFirst");
}
export function discardKeepsItHidden() {
  return t("web.collections.copy.discardKeepsItHidden");
}
export function addToHome() {
  return t("web.collections.copy.addToHome");
}
export function addToHomeHelp() {
  return t("web.collections.copy.addToHomeHelp");
}
export function addToLibraryPage() {
  return t("web.collections.copy.addToLibraryPage");
}
export function addToLibraryPageHelp() {
  return t("web.collections.copy.addToLibraryPageHelp");
}
export function openRow() {
  return t("web.collections.copy.openRow");
}

// Personal collections: rows on the viewer's own Home and library pages.
export function noMyRowsYet() {
  return t("web.collections.copy.noMyRowsYet");
}
export function myRowsOnceCreated() {
  return t("web.collections.copy.myRowsOnceCreated");
}
export function addToMyHome() {
  return t("web.collections.copy.addToMyHome");
}
export function addToMyHomeItem() {
  return t("web.collections.copy.addToMyHomeItem");
}
export function addToMyHomeHelp() {
  return t("web.collections.copy.addToMyHomeHelp");
}
export function addToMyLibraryPage() {
  return t("web.collections.copy.addToMyLibraryPage");
}

export function rowCountLabel(count: number): string {
  return t("web.collections.copy.rowCount", { count, count_text: count });
}

/** "Kids page": the rows above a library's grid. */
export function libraryPageLabel(libraryName: string): string {
  return t("web.collections.copy.libraryPage", { library: libraryName });
}

export function saveFirstTitle() {
  return t("web.collections.copy.saveFirstTitle");
}
/** Save and continue waits, like Save, for a choice on each field changed in both places. */
export function saveAfterConflicts() {
  return t("web.collections.copy.saveAfterConflicts");
}
/** Save and continue waits, like Save, for changes that can be saved. */
export function saveNotReady() {
  return t("web.collections.copy.saveNotReady");
}

/** "You're about to add Studio Ghibli as a row on Home. Name not saved yet." */
export function saveFirstDescription(
  name: string,
  where: string,
  fieldLabels: readonly string[],
  titlesSaved: boolean,
): string {
  const parts = [t("web.collections.copy.saveFirstDescription", { name, where })];
  if (fieldLabels.length > 0)
    parts.push(t("web.collections.copy.notSavedYet", { fields: lowerNames(fieldLabels) }));
  if (titlesSaved) parts.push(titlesAlreadySaved());
  return parts.join(" ");
}

/** Deleting a server collection rows show: the rows go first. */
export function deleteWithRowsDescription(
  libraryNames: readonly string[],
  rowCount: number,
): string {
  const removed =
    libraryNames.length > 0
      ? t("web.collections.copy.removedFromForEveryone", { libraries: joinNames(libraryNames) })
      : t("web.collections.copy.removedForEveryone");
  return `${removed} ${t("web.collections.copy.rowsShowIt", { count: rowCount, count_text: rowCount })}`;
}

export function keepRowsHint() {
  return t("web.collections.copy.keepRowsHint");
}
export function titlesStay() {
  return t("web.collections.copy.titlesStay");
}
export function checkingRows() {
  return t("web.collections.copy.checkingRows");
}

export function deleteWithRowsLabel(rowCount: number): string {
  return t("web.collections.copy.deleteWithRowsLabel", {
    count: rowCount,
    count_text: rowCount,
  });
}

/** A delete with rows found the collection changed since the editor read it: nothing went. */
export function changedBeforeDelete() {
  return t("web.collections.copy.changedBeforeDelete");
}
/** A delete with rows couldn't read the collection first: nothing went. */
export function checkBeforeDeleteFailed() {
  return t("web.collections.copy.checkBeforeDeleteFailed");
}

/** A delete with rows that stopped part way: the collection stays. */
export function rowsLeftMessage(rows: readonly string[]): string {
  return t("web.collections.copy.rowsLeft", { rows: joinNames(rows) });
}

// --- Select mode and Delete all ---------------------------------------------

/** Rows use every collection a delete was asked for, so nothing goes. */
export function collectionsInUse() {
  return t("web.collections.copy.collectionsInUse");
}

/** A starter pack being added would race a delete of the collections it makes. */
export function starterPackBlocksDelete() {
  return t("web.collections.copy.starterPackBlocksDelete");
}

/** Up to five names, then how many more. */
function someNames(names: readonly string[]): string {
  const more = names.length - 4;
  const shown =
    names.length > 5
      ? [
          ...names.slice(0, 4),
          t("web.collections.copy.moreNames", { count: more, count_text: more }),
        ]
      : names;
  return joinNames(shown);
}

export function syncListsLabel(count: number): string {
  return t("web.collections.copy.syncLists", { count, count_text: count });
}

/** Sync works on synced lists only; says how many picked collections it passes over. */
export function syncSkipNote(smart: number, manual: number): string | null {
  if (smart + manual === 0) return null;
  const kinds = smart && manual ? "both" : smart ? "smart" : "manual";
  return t("web.collections.copy.syncSkipNote", { kinds, count: smart + manual });
}

export type BatchAction = "sync" | "show" | "hide";

/**
 * The toast after a select-mode action: all done, some done, or none.
 * `warned` counts the done ones that finished with warnings (a sync's unmatched entries).
 */
export function batchResult(
  action: BatchAction,
  done: number,
  total: number,
  warned = 0,
): { tone: "success" | "warning" | "error"; message: string } {
  const warnings = warned;
  if (done === total)
    return {
      tone: warned > 0 ? "warning" : "success",
      message:
        warned > 0
          ? t("web.collections.copy.batchAllWarned", {
              action,
              count: done,
              count_text: done,
              warned: warnings,
            })
          : t("web.collections.copy.batchAll", { action, count: done, count_text: done }),
    };
  if (done > 0)
    return {
      tone: "warning",
      message:
        warned > 0
          ? t("web.collections.copy.batchSomeWarned", {
              action,
              done: done,
              count: total,
              count_text: total,
              warned: warnings,
            })
          : t("web.collections.copy.batchSome", {
              action,
              done: done,
              count: total,
              count_text: total,
            }),
    };
  return {
    tone: "error",
    message: t("web.collections.copy.batchNone", {
      action,
      count: total,
      count_text: total,
    }),
  };
}

export function alreadyShown(shown: boolean): string {
  return shown
    ? t("web.collections.copy.alreadyShownOnTabs")
    : t("web.collections.copy.alreadyHidden");
}

export function hideCollectionsTitle(count: number): string {
  return t("web.collections.copy.hideCollectionsTitle", { count, count_text: count });
}

/** Hiding several collections rows show: `rowCount` is the rows' total. */
export function hideCollectionsDescription(rowCount: number): string {
  return t("web.collections.copy.hideCollectionsDescription", {
    count: rowCount,
    count_text: rowCount,
  });
}

/** "Delete 7 synced lists in Movies?" `where` is a library name or "this view". */
export function deleteCollectionsTitle(
  count: number,
  kind: CollectionKind | null,
  where: string | null,
): string {
  const values = { kind: kind ?? "mixed", count, count_text: count };
  return where
    ? t("web.collections.copy.deleteCollectionsTitleIn", { ...values, where })
    : t("web.collections.copy.deleteCollectionsTitle", values);
}

export function deleteCollectionsDescription(count: number): string {
  return t("web.collections.copy.deleteCollectionsDescription", { count });
}

/** A collection is one thing in every library it's in, so deleting it there removes it everywhere. */
export function alsoDeletedElsewhere(
  entries: ReadonlyArray<{ title: string; libraryNames: readonly string[] }>,
  kind: CollectionKind | null,
): string {
  const names = someNames(
    entries.map((entry) => nameWithPlace(entry.title, joinNames(entry.libraryNames))),
  );
  return t("web.collections.copy.alsoDeletedElsewhere", {
    kind: kind ?? "mixed",
    count: entries.length,
    names,
  });
}

/** "Studio Ghibli (Home)": a name and where it is, for naming things in a message. */
export function nameWithPlace(name: string, place: string): string {
  return t("web.collections.copy.nameWithPlace", { name, place });
}

/** The collections a delete leaves alone because Home or library page rows show them. */
export function keptForRows(titles: readonly string[]): string {
  return t("web.collections.copy.keptForRows", {
    count: titles.length,
    count_text: titles.length,
    names: someNames(titles),
  });
}

// --- Titles -----------------------------------------------------------------

const TITLES_CAPTION: Readonly<Record<ScopeKind | "create", MessageKey>> = {
  server: "web.collections.copy.titlesCaptionServer",
  personal: "web.collections.copy.titlesCaptionPersonal",
  create: "web.collections.copy.titlesCaptionCreate",
};

export function titlesCaption(kind: ScopeKind | "create"): string {
  return t(TITLES_CAPTION[kind]);
}
export function titlesSavedLabel() {
  return t("web.collections.copy.titlesSaved");
}
export function titlesEmpty() {
  return t("web.collections.copy.titlesEmpty");
}
export function inThisCollection() {
  return t("web.collections.copy.inThisCollection");
}
export function allYourLibraries() {
  return t("web.collections.copy.allYourLibraries");
}
export function manualOrderLine() {
  return t("web.collections.copy.manualOrderLine");
}

export function removedTitle(title: string): string {
  return t("web.collections.copy.removedTitle", { title });
}

export function createdButNotAdded(count: number): string {
  return t("web.collections.copy.createdButNotAdded", { count, count_text: count });
}

// --- Add to collection -----------------------------------------------------

export function addToCollectionFootnote() {
  return t("web.collections.copy.addToCollectionFootnote");
}

/** "Manual · 15 titles": a manual collection's line in a picker. */
export function manualTitleCount(count: number): string {
  return t("web.collections.copy.manualTitleCount", {
    count,
    count_text: count.toLocaleString(formatLocale()),
  });
}

/** The Add to collection footer: how many of the profile's collections hold the title. */
export function inCollections(count: number): string {
  return t("web.collections.copy.inCollections", { count, count_text: count });
}

/** The new collection was kept, but the title it was made for didn't go in. */
export function madeButNotAdded(collection: string, title: string): string {
  return t("web.collections.copy.madeButNotAdded", { collection, title });
}

// --- Synced list ------------------------------------------------------------

export function syncedHeading() {
  return t("web.collections.copy.syncedHeading");
}
export function syncedCaption() {
  return t("web.collections.copy.syncedCaption");
}
export function syncedCreateSubtitle() {
  return t("web.collections.copy.syncedCreateSubtitle");
}
export function syncedOff() {
  return t("web.collections.copy.syncedOff");
}
export function popularPicks() {
  return t("web.collections.copy.popularPicks");
}
export function popularPicksHelp() {
  return t("web.collections.copy.popularPicksHelp");
}
export function fromMdblist() {
  return t("web.collections.copy.fromMdblist");
}
export function fromMdblistHelp() {
  return t("web.collections.copy.fromMdblistHelp");
}
export function pasteMdblistLink() {
  return t("web.collections.copy.pasteMdblistLink");
}
export function mdblistLinkInvalid() {
  return t("web.collections.copy.mdblistLinkInvalid");
}
export function mdblistSearchOffProfile() {
  return t("web.collections.copy.mdblistSearchOffProfile");
}
export function pasteTmdbListLink() {
  return t("web.collections.copy.pasteTmdbListLink");
}
export function tmdbListHelp() {
  return t("web.collections.copy.tmdbListHelp");
}
export function chartRulesNote() {
  return t("web.collections.copy.chartRulesNote");
}
export function nameFilledHelp() {
  return t("web.collections.copy.nameFilledHelp");
}
export function syncsOnCreate() {
  return t("web.collections.copy.syncsOnCreate");
}
export function pickAListFirst() {
  return t("web.collections.copy.pickAListFirst");
}
export function syncedOrderHelp() {
  return t("web.collections.copy.syncedOrderHelp");
}

/** "Kept your name", "Kept your name and description": what a new pick left alone. */
export function keptMessage(fields: readonly ("name" | "description")[]): string | null {
  const name = fields.includes("name");
  const description = fields.includes("description");
  if (name && description) return t("web.collections.copy.keptNameAndDescription");
  if (name) return t("web.collections.copy.keptName");
  if (description) return t("web.collections.copy.keptDescription");
  return null;
}

/** "This list only has movies, so TV Shows isn't offered." */
export function ineligibleLibrariesLine(
  mediaKind: "movie" | "tv",
  libraryNames: readonly string[],
): string {
  return t("web.collections.copy.ineligibleLibraries", {
    count: libraryNames.length,
    kind: mediaKind,
    libraries: joinNames(libraryNames),
  });
}

/** "Server time (UTC−5)", with the zone's name when the server reports one. */
export function serverTimeLabel(zone?: { utc_offset: string; name?: string }): string {
  if (!zone) return t("web.collections.copy.serverTime");
  const match = /^([+-])(\d{2}):(\d{2})$/.exec(zone.utc_offset);
  let offset = "UTC";
  if (match && (match[2] !== "00" || match[3] !== "00")) {
    const hours = String(Number(match[2]));
    offset = `UTC${match[1] === "-" ? "−" : "+"}${hours}${match[3] === "00" ? "" : `:${match[3]}`}`;
  }
  return zone.name
    ? t("web.collections.copy.serverTimeNamed", { offset, zone: zone.name })
    : t("web.collections.copy.serverTimeOffset", { offset });
}

/** The toast after a synced list is created: how its first sync went. */
export function firstSyncMessage(sync?: { status: string; message: string; itemsMatched: number }) {
  if (!sync)
    return { tone: "success" as const, text: t("web.collections.copy.firstSyncScheduled") };
  if (sync.status === "failed") {
    return {
      tone: "warning" as const,
      text: t("web.collections.copy.firstSyncFailed", { message: sync.message }).trim(),
    };
  }
  if (sync.status === "warning") {
    return {
      tone: "warning" as const,
      text: t("web.collections.copy.firstSyncWarned", {
        count: sync.itemsMatched,
        count_text: sync.itemsMatched,
      }),
    };
  }
  return {
    tone: "success" as const,
    text: t("web.collections.copy.firstSyncFound", {
      count: sync.itemsMatched,
      count_text: sync.itemsMatched,
    }),
  };
}

/** "3 hours ago": how long ago a sync ran. */
export function syncedAgo(iso: string, now = Date.now()): string {
  const seconds = Math.round((now - Date.parse(iso)) / 1000);
  if (Number.isNaN(seconds)) return t("web.collections.copy.agoRecently");
  if (seconds < 60) return t("web.collections.copy.agoJustNow");
  const steps: Array<[number, MessageKey]> = [
    [60, "web.collections.copy.agoMinutes"],
    [24, "web.collections.copy.agoHours"],
    [30, "web.collections.copy.agoDays"],
    [12, "web.collections.copy.agoMonths"],
  ];
  let value = Math.round(seconds / 60);
  for (const [size, unit] of steps) {
    if (value < size) return t(unit, { count: value, count_text: value });
    value = Math.round(value / size);
  }
  return t("web.collections.copy.agoYears", { count: value, count_text: value });
}

/** "41 titles skipped". */
export function titlesSkipped(count: number): string {
  return t("web.collections.copy.titlesSkipped", { count, count_text: count });
}

/** Why a synced list skips titles, and what brings them in. */
export function skippedExplanation(
  count: number | undefined,
  libraryNames: readonly string[],
  canSync = true,
) {
  const where =
    libraryNames.length > 0 ? joinNames(libraryNames) : t("web.collections.copy.yourLibraries");
  if (count === undefined) {
    return canSync
      ? t("web.collections.copy.skippedUncountedSync", { where })
      : t("web.collections.copy.skippedUncounted", { where });
  }
  return t("web.collections.copy.skippedCounted", { count, count_text: count, where });
}

/** The red callout at the top of a list whose last sync failed. */
export function syncFailedLead(lastAt: string | undefined): string {
  return lastAt
    ? t("web.collections.copy.syncFailedAgo", { ago: syncedAgo(lastAt) })
    : t("web.collections.copy.syncFailed");
}

export function keepsTitles(count: number): string {
  return t("web.collections.copy.keepsTitles", { count, count_text: count });
}

export function titlesReadyToAdd(count: number): string {
  return t("web.collections.copy.titlesReadyToAdd", { count, count_text: count });
}

/** "Name and description": the first field as labelled, the rest in lower case. */
function lowerNames(fieldLabels: readonly string[]): string {
  const [first, ...rest] = fieldLabels;
  if (!first) return "";
  // Lower-casing a label inside a sentence is English casing; other languages
  // keep their labels as written (German nouns stay capitalized).
  if (appLanguage() !== "en") return joinNames(fieldLabels);
  return joinNames([first, ...rest.map((label) => label.charAt(0).toLowerCase() + label.slice(1))]);
}

/** "Name and description not saved". */
export function notSavedMessage(fieldLabels: readonly string[]): string {
  if (fieldLabels.length === 0) return "";
  return t("web.collections.copy.notSaved", { fields: lowerNames(fieldLabels) });
}

// --- Conflicts and deletes --------------------------------------------------

export function conflictTitle() {
  return t("web.collections.copy.conflictTitle");
}

export function serverDeleteDescription(libraryNames: readonly string[]): string {
  if (libraryNames.length === 0) return t("web.collections.copy.serverDeleteDescriptionNone");
  return t("web.collections.copy.serverDeleteDescription", {
    libraries: joinNames(libraryNames),
  });
}

export function personalDeleteDescription(shared: boolean): string {
  return shared
    ? t("web.collections.copy.personalDeleteDescriptionShared")
    : t("web.collections.copy.cantBeUndone");
}

/** Deleting a shelf never deletes its collections, and touches one library only. */
export function deleteShelfDescription(collectionCount: number, libraryName: string): string {
  const members = t("web.collections.copy.deleteShelfMembers", {
    count: collectionCount,
    count_text: collectionCount,
    library: libraryName,
  });
  return `${members} ${t("web.collections.copy.deleteShelfOnly", { library: libraryName })}`;
}
