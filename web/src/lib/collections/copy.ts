import type { DraftField } from "./draft";
import type { ArtworkSlot, ScopeKind } from "./scope";
import { COLLECTION_KIND_LABEL, type CollectionKind } from "./types";

/**
 * Words every collections surface shares, so the editor, lists and dialogs
 * say the same thing. "Collections tab" is where a library's collections are
 * browsed; "library page" is the rows above a library's grid.
 */

/** "Movies", "Movies and Kids", "Movies, Kids and 4K Movies". */
export function joinNames(names: readonly string[]): string {
  if (names.length <= 1) return names[0] ?? "";
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

function plural(count: number, word: string) {
  return `${count} ${word}${count === 1 ? "" : "s"}`;
}

// --- New collection ---------------------------------------------------------

export const NEW_COLLECTION = "New collection";

/** The type picker's subtitle: what the type decides, and what the editor asks next. */
export const NEW_COLLECTION_HELP: Readonly<Record<ScopeKind, string>> = {
  server: "What decides what's in it? Next you'll name it, fill it and choose where it shows.",
  personal:
    "What decides what's in it? Only you can change it. Next you'll name it, fill it and choose who sees it.",
};

export const NEW_COLLECTION_NEXT = "Next: name it and fill it in, on its own page.";
export const STARTER_PACK_PROMPT = "Want a whole set at once?";
export const ADD_A_STARTER_PACK = "Add a starter pack";

/** What each type does, under its name on the type picker. */
export const KIND_SENTENCE: Readonly<Record<CollectionKind, string>> = {
  manual: "You pick the titles and put them in order.",
  smart: "Titles that match your rules. It fills itself and keeps up as titles are added.",
  synced: "Follows a list from MDBList or TMDB and updates on a schedule.",
};

/** Examples for each type on the type picker; a profile has no staff. */
export const KIND_GOOD_FOR: Readonly<Record<ScopeKind, Readonly<Record<CollectionKind, string>>>> =
  {
    server: {
      manual: "staff picks, a director's best, movie night.",
      smart: "90s comedies, unwatched 4K, Christmas movies.",
      synced: "IMDb Top 250, Netflix Originals, trending this week.",
    },
    personal: {
      manual: "a director's best, movie night, a watch order.",
      smart: "90s comedies, unwatched 4K, Christmas movies.",
      synced: "IMDb Top 250, Netflix Originals, trending this week.",
    },
  };

export const SYNCED_CHECKING = "Checking Synced lists…";
export const SYNCED_CHECK_FAILED = "Couldn't check whether Synced lists are on.";

// --- Where it shows ---------------------------------------------------------

export const SHOW_ON_TAB_LABEL = "Show on the Collections tab";

/** The server switch's help: where viewers find the collection. */
export function serverTabHelp(libraryNames: readonly string[]): string {
  if (libraryNames.length === 0) return "Viewers find it on its libraries' Collections tabs.";
  return `Viewers find it under ${joinNames(libraryNames.map((name) => `${name} › Collections`))}.`;
}

/**
 * The personal switch's help. Profiles the collection is shared with see it
 * on their Collections tabs too, so the label can't say "my".
 */
export const PERSONAL_TAB_HELP = "For you and anyone you share it with";

export const SHOW_TO_OTHER_PROFILES_LABEL = "Show to other profiles";
export const SHOW_TO_OTHER_PROFILES_HELP =
  "Every profile on this account sees it, minus titles it can't access. Nobody else on the server can see it.";

/** What turning sharing off costs: "Maya and Leo lose it, including rows they made from it." */
export function unshareConsequence(profileNames: readonly string[]): string {
  const who = profileNames.length > 0 ? joinNames(profileNames) : "Other profiles";
  const verb = profileNames.length === 1 ? "loses" : "lose";
  return `${who} ${verb} it, including rows they made from it.`;
}

/** Shown when sharing is turned off on a saved collection. */
export function unshareWarning(profileNames: readonly string[]): string {
  return `When you save, ${unshareConsequence(profileNames)}`;
}

/** The ⋯ switch's help on a card: the menu has room for one short line. */
export const SHOW_TO_OTHER_PROFILES_SHORT_HELP = "Every profile on this account sees it";

/** The Libraries row while a server collection has none ticked. */
export const LIBRARIES_NOT_PICKED = "Not picked yet";
/** The Shelf row of a collection that isn't created yet, after "No heading". */
export const SHELF_AFTER_CREATE = "Move it in Arrange once it's created";

// --- Server list ------------------------------------------------------------

export const ON_HOME = "On Home";
export const HIDDEN_FROM_TAB = "Hidden from Collections tab";

/** "Movies › Collections and Kids › Collections". */
function collectionsTabs(libraryNames: readonly string[]): string {
  return joinNames(libraryNames.map((name) => `${name} › Collections`));
}

export function hideCollectionTitle(name: string): string {
  return `Hide ${name} from Collections tabs?`;
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
  const leaves = libraryNames.length > 0 ? `It leaves ${collectionsTabs(libraryNames)}. ` : "";
  const on = places ? ` on ${places}` : "";
  if (rowCount !== 1)
    return `${leaves}${rowCount} rows${on} still show it, but their See all won't open while it's hidden.`;
  if (places)
    return `${leaves}1 row${on} still shows it, but that row's See all won't open while it's hidden.`;
  return `${leaves}1 row still shows it, but its See all won't open while it's hidden.`;
}

/** A delete the server refused because Home or library page rows still show the collection. */
export const COLLECTION_IN_USE = "Rows still use it. Remove them first.";

// --- Where it shows: rows ---------------------------------------------------

export const ROWS_THAT_SHOW_IT = "Rows that show it";
export const ROWS_NOT_LISTED = "Rows profiles add to their own Home aren't listed.";
export const NO_ROWS_YET = "No Home or library page row shows it yet.";
export const ROWS_FAILED = "Couldn't load the rows that show it.";
export const ADD_AS_A_ROW = "Add as a row";
export const OTHER_LIBRARIES = "Other libraries";
/** The Rows that show it row of a collection that isn't created yet. */
export const ROWS_ONCE_CREATED = "None yet. Add it to Home or a library page once it's created.";
export const SHOW_IT_FIRST = "Show it on the Collections tab first. Viewers couldn't open See all.";
export const DISCARD_KEEPS_IT_HIDDEN =
  "It's saved as hidden from the Collections tab, so save to show it before adding it as a row.";
export const ADD_TO_HOME = "Add to Home…";
export const ADD_TO_HOME_HELP = "A row on everyone's Home";
export const ADD_TO_LIBRARY_PAGE = "Add to a library page…";
export const ADD_TO_LIBRARY_PAGE_HELP = "A row above a library's grid";
export const OPEN_ROW = "Open row";

// Personal collections: rows on the viewer's own Home and library pages.
export const NO_MY_ROWS_YET = "None of your Home or library page rows show it yet.";
export const MY_ROWS_ONCE_CREATED =
  "None yet. Add it to your Home or a library page once it's created.";
export const ADD_TO_MY_HOME = "Add to my Home";
export const ADD_TO_MY_HOME_ITEM = "Add to my Home…";
export const ADD_TO_MY_HOME_HELP = "A row on your Home";
export const ADD_TO_MY_LIBRARY_PAGE = "Add to my library page";

export function rowCountLabel(count: number): string {
  return plural(count, "row");
}

/** "Kids page": the rows above a library's grid. */
export function libraryPageLabel(libraryName: string): string {
  return `${libraryName} page`;
}

export const SAVE_FIRST_TITLE = "Save changes first?";
/** Save and continue waits, like Save, for a choice on each field changed in both places. */
export const SAVE_AFTER_CONFLICTS =
  "Some fields changed here and elsewhere. Choose Keep mine or Use theirs for each, then save.";
/** Save and continue waits, like Save, for changes that can be saved. */
export const SAVE_NOT_READY = "These changes can't be saved yet. The save bar says what's missing.";

/** "You're about to add Studio Ghibli as a row on Home. Name not saved yet." */
export function saveFirstDescription(
  name: string,
  where: string,
  fieldLabels: readonly string[],
  titlesSaved: boolean,
): string {
  const pending = fieldLabels.length > 0 ? ` ${notSavedMessage(fieldLabels)} yet.` : "";
  return `You're about to add ${name} as a row on ${where}.${pending}${titlesSaved ? ` ${TITLES_ALREADY_SAVED}` : ""}`;
}

/** Deleting a server collection rows show: the rows go first. */
export function deleteWithRowsDescription(
  libraryNames: readonly string[],
  rowCount: number,
): string {
  const removed =
    libraryNames.length > 0
      ? `It's removed from ${joinNames(libraryNames)} for everyone.`
      : "It's removed for everyone.";
  return `${removed} ${rowCount === 1 ? "1 row shows it" : `${rowCount} rows show it`}:`;
}

export const KEEP_ROWS_HINT =
  "To keep the rows, open each one and point it at another collection, then come back.";
export const TITLES_STAY = "Titles stay in your libraries.";
export const CHECKING_ROWS = "Checking which rows show it…";

export function deleteWithRowsLabel(rowCount: number): string {
  return `Delete it and its ${rowCountLabel(rowCount)}`;
}

/** A delete with rows found the collection changed since the editor read it: nothing went. */
export const CHANGED_BEFORE_DELETE =
  "This collection changed since you opened it, so nothing was deleted. Check it, then delete again.";
/** A delete with rows couldn't read the collection first: nothing went. */
export const CHECK_BEFORE_DELETE_FAILED =
  "Couldn't check the collection before deleting its rows, so nothing was deleted. Try again.";

/** A delete with rows that stopped part way: the collection stays. */
export function rowsLeftMessage(rows: readonly string[]): string {
  return `Couldn't delete every row, so the collection was kept. Still showing it: ${joinNames(rows)}.`;
}

// --- Select mode and Delete all ---------------------------------------------

/** Rows use every collection a delete was asked for, so nothing goes. */
export const COLLECTIONS_IN_USE = "Rows still use them. Remove the rows first.";

/** A starter pack being added would race a delete of the collections it makes. */
export const STARTER_PACK_BLOCKS_DELETE = "A starter pack is being added. Delete once it finishes.";

const KIND_NOUN: Readonly<Record<CollectionKind | "mixed", readonly [string, string]>> = {
  manual: ["manual collection", "manual collections"],
  smart: ["smart collection", "smart collections"],
  synced: ["synced list", "synced lists"],
  mixed: ["collection", "collections"],
};

/** "1 synced list", "7 collections": `kind` null when the collections are of several types. */
function kindCount(count: number, kind: CollectionKind | null): string {
  const [one, many] = KIND_NOUN[kind ?? "mixed"];
  return `${count} ${count === 1 ? one : many}`;
}

/** Up to five names, then how many more. */
function someNames(names: readonly string[]): string {
  const shown = names.length > 5 ? [...names.slice(0, 4), `${names.length - 4} more`] : names;
  return joinNames(shown);
}

export function syncListsLabel(count: number): string {
  return `Sync ${plural(count, "list")}`;
}

/** Sync works on synced lists only; says how many picked collections it passes over. */
export function syncSkipNote(smart: number, manual: number): string | null {
  if (smart + manual === 0) return null;
  const kinds = smart && manual ? "smart and manual" : smart ? "smart" : "manual";
  return `Sync skips ${kinds} collections (${smart + manual} here).`;
}

export type BatchAction = "sync" | "show" | "hide";

const BATCH_WORDS: Readonly<
  Record<BatchAction, { done: string; verb: string; noun: string; where: string }>
> = {
  sync: { done: "Synced", verb: "sync", noun: "list", where: "" },
  show: { done: "Showed", verb: "show", noun: "collection", where: " on Collections tabs" },
  hide: { done: "Hid", verb: "hide", noun: "collection", where: " from Collections tabs" },
};

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
  const words = BATCH_WORDS[action];
  const warnings = warned > 0 ? `, ${warned} with warnings` : "";
  if (done === total)
    return {
      tone: warned > 0 ? "warning" : "success",
      message: `${words.done} ${plural(done, words.noun)}${words.where}${warnings}.`,
    };
  if (done > 0)
    return {
      tone: "warning",
      message: `${words.done} ${done} of ${plural(total, words.noun)}${warnings}.`,
    };
  return { tone: "error", message: `Couldn't ${words.verb} ${plural(total, words.noun)}.` };
}

export function alreadyShown(shown: boolean): string {
  return shown
    ? "The selected collections are already on Collections tabs."
    : "The selected collections are already hidden.";
}

export function hideCollectionsTitle(count: number): string {
  return `Hide ${count} collections from Collections tabs?`;
}

/** Hiding several collections rows show: `rowCount` is the rows' total. */
export function hideCollectionsDescription(rowCount: number): string {
  return rowCount === 1
    ? "1 row still shows one of them, but its See all won't open while it's hidden."
    : `${rowCount} rows still show them, but those rows' See all won't open while they're hidden.`;
}

/** "Delete 7 synced lists in Movies?" `where` is a library name or "this view". */
export function deleteCollectionsTitle(
  count: number,
  kind: CollectionKind | null,
  where: string | null,
): string {
  return `Delete ${kindCount(count, kind)}${where ? ` in ${where}` : ""}?`;
}

export function deleteCollectionsDescription(count: number): string {
  return count === 1
    ? "It's removed for everyone. Its titles stay in your libraries."
    : "They're removed for everyone. Their titles stay in your libraries.";
}

/** A collection is one thing in every library it's in, so deleting it there removes it everywhere. */
export function alsoDeletedElsewhere(
  entries: ReadonlyArray<{ title: string; libraryNames: readonly string[] }>,
  kind: CollectionKind | null,
): string {
  const [one, many] = KIND_NOUN[kind ?? "mixed"];
  const names = someNames(
    entries.map((entry) => `${entry.title} (${joinNames(entry.libraryNames)})`),
  );
  return entries.length === 1
    ? `A ${one} that's also in another library goes there too: ${names}.`
    : `${many.charAt(0).toUpperCase()}${many.slice(1)} that are also in other libraries go there too: ${names}.`;
}

/** The collections a delete leaves alone because Home or library page rows show them. */
export function keptForRows(titles: readonly string[]): string {
  return titles.length === 1
    ? `1 is kept because rows use it: ${titles[0]}.`
    : `${titles.length} are kept because rows use them: ${someNames(titles)}.`;
}

// --- Titles -----------------------------------------------------------------

export const TITLES_CAPTION = {
  server: "In the order viewers see them. Titles save as you add, remove or drag them.",
  personal: "In the order you see them. Titles save as you add, remove or drag them.",
  create: "Pick titles now. They're added when you press Create collection.",
} as const;
export const TITLES_SAVED = "Saved";
export const TITLES_EMPTY = "No titles yet. Search above to add the first.";
export const IN_THIS_COLLECTION = "In this collection";
export const ALL_YOUR_LIBRARIES = "All your libraries";
export const MANUAL_ORDER_LINE = "Your order. Drag titles to change it.";

export function removedTitle(title: string): string {
  return `Removed ${title}`;
}

export function createdButNotAdded(count: number): string {
  return `Created, but couldn't add ${plural(count, "title")}`;
}

// --- Add to collection -----------------------------------------------------

export const ADD_TO_COLLECTION_FOOTNOTE =
  "Only manual collections take titles by hand. Ticking saves right away.";

/** "Manual · 15 titles": a manual collection's line in a picker. */
export function manualTitleCount(count: number): string {
  return `${COLLECTION_KIND_LABEL.manual} · ${plural(count, "title")}`;
}

/** The Add to collection footer: how many of the profile's collections hold the title. */
export function inCollections(count: number): string {
  return count === 0 ? "Not in a collection yet" : `In ${plural(count, "collection")}`;
}

/** The new collection was kept, but the title it was made for didn't go in. */
export function madeButNotAdded(collection: string, title: string): string {
  return `Made ${collection}, but couldn't add ${title}`;
}

// --- Rules (Smart) ----------------------------------------------------------

export const RULES_CAPTION =
  "Titles that match are in the collection. New matches join on their own.";
/** Under a personal Smart collection's rules. */
export const PERSONAL_RULES_NOTE =
  "“All my libraries” follows the libraries this profile can see. Rules about you, like Watched, are offered only here.";
export const ALL_MY_LIBRARIES = "all my libraries";
/** A personal list's libraries when none is ticked, as the library menu reads. */
export const ALL_MY_LIBRARIES_LABEL = "All my libraries";
/** The meta line's last part on a Smart collection. */
export const SMART_UPDATES_ITSELF = "Updates itself as titles are added";
/** After "Rules not saved" in the save bar. */
export const PREVIEW_SHOWS_UNSAVED = "The preview already shows them.";

export const PREVIEW_LIVE = "Live preview";
/** Beside the preview count while a server collection has no library picked yet. */
export const PREVIEW_EVERY_LIBRARY = "From every library until you pick some";
export const PREVIEW_EMPTY = "No titles match yet";
export const PREVIEW_EMPTY_HELP = "You can still save. Titles that match later join on their own.";
export const PREVIEW_FAILED = "The preview didn't load. You can still save.";

export function previewMatches(total: number): string {
  return total === 1 ? "1 title matches" : `${total.toLocaleString()} titles match`;
}

/** The save bar's hint on a new Smart collection, once the preview has answered. */
export function smartCreateHint(total: number): string {
  if (total === 0) return "Nothing matches yet. Titles that match later join on their own.";
  return `${previewMatches(total)} now. New ones join on their own.`;
}

// --- Order ------------------------------------------------------------------

export const ORDER_HELP = "A profile that picks its own sort while browsing keeps that choice.";
export const NO_LIMIT = "No limit";

/** A smart collection's stored default sort, which wins over Order until cleared. */
export function storedSortLine(sortLabel: string): string {
  return `A saved default sort, ${sortLabel}, wins over this Order.`;
}

// --- Synced list ------------------------------------------------------------

export const SYNCED_HEADING = "The list it follows";
export const SYNCED_CAPTION = "Titles come from this list and update on its schedule.";
export const SYNCED_CREATE_SUBTITLE =
  "Pick the list, check the details, then press Create collection.";
export const SYNCED_OFF = "Synced lists are off on this server.";
export const POPULAR_PICKS = "Popular picks";
export const POPULAR_PICKS_HELP = "Ready-made lists that work well here";
export const FROM_MDBLIST = "From MDBList";
export const FROM_MDBLIST_HELP = "Public lists by other people";
export const PASTE_MDBLIST_LINK = "Or paste any MDBList link";
export const MDBLIST_LINK_INVALID =
  "Paste the link to a list on mdblist.com, like https://mdblist.com/lists/…";
export const MDBLIST_SEARCH_OFF_PROFILE =
  "Searching MDBList is off on this server. Popular picks and pasted links still work.";
export const PASTE_TMDB_LIST_LINK = "Paste a TMDB list link";
export const TMDB_LIST_HELP = "Public lists only. Its titles arrive with the first sync.";
export const CHART_RULES_NOTE = "“Trending over” and “Both” appear only for Trending.";
export const NAME_FILLED_HELP =
  "Filled in from the list. Picking another list keeps anything you typed.";
export const SYNCS_ON_CREATE = "It syncs for the first time when you create it.";
export const PICK_A_LIST_FIRST = "Pick a list, then create it.";
export const SYNCED_ORDER_HELP = "Blank takes the whole list, up to 500.";

/** "Kept your name", "Kept your name and description": what a new pick left alone. */
export function keptMessage(fields: readonly ("name" | "description")[]): string | null {
  return fields.length > 0 ? `Kept your ${joinNames([...fields])}` : null;
}

/** "This list only has movies, so TV Shows isn't offered." */
export function ineligibleLibrariesLine(
  mediaKind: "movie" | "tv",
  libraryNames: readonly string[],
): string {
  const only = mediaKind === "movie" ? "movies" : "TV shows";
  const verb = libraryNames.length === 1 ? "isn't" : "aren't";
  return `This list only has ${only}, so ${joinNames(libraryNames)} ${verb} offered.`;
}

/** "Server time (UTC−5)", with the zone's name when the server reports one. */
export function serverTimeLabel(zone?: { utc_offset: string; name?: string }): string {
  if (!zone) return "Server time";
  const match = /^([+-])(\d{2}):(\d{2})$/.exec(zone.utc_offset);
  let offset = "UTC";
  if (match && (match[2] !== "00" || match[3] !== "00")) {
    const hours = String(Number(match[2]));
    offset = `UTC${match[1] === "-" ? "−" : "+"}${hours}${match[3] === "00" ? "" : `:${match[3]}`}`;
  }
  return `Server time (${zone.name ? `${offset}, ${zone.name}` : offset})`;
}

/** The toast after a synced list is created: how its first sync went. */
export function firstSyncMessage(sync?: { status: string; message: string; itemsMatched: number }) {
  if (!sync) return { tone: "success" as const, text: "Created. It syncs on its schedule." };
  const titles = `${sync.itemsMatched} title${sync.itemsMatched === 1 ? "" : "s"}`;
  if (sync.status === "failed") {
    return {
      tone: "warning" as const,
      text: `Created, but the first sync failed. ${sync.message}`.trim(),
    };
  }
  if (sync.status === "warning") {
    return {
      tone: "warning" as const,
      text: `Created. The first sync found ${titles}, with warnings.`,
    };
  }
  return { tone: "success" as const, text: `Created. The first sync found ${titles}.` };
}

// --- A saved Synced list ----------------------------------------------------

export const SYNC_NOW = "Sync now";
export const SYNCING_NOW = "Syncing now…";
/** Why Sync now waits: it runs the saved list, not unsaved changes to it. */
export const SAVE_BEFORE_SYNC = "Save your changes first; a sync runs the saved list.";
export const LAST_SYNC = "Last sync";
export const NEXT_SYNC = "Next sync";
export const NOT_IN_YOUR_LIBRARIES = "Not in your libraries";
export const NOT_SYNCED_YET = "Not yet";
export const NOT_SCHEDULED = "Not scheduled";
export const NOT_COUNTED_YET = "Not counted yet";
export const WHY_SKIPPED = "Why titles are skipped";
export const CHANGE_LINK = "Change link";
export const MDBLIST_LINK = "MDBList link";
export const TMDB_LIST_LINK = "TMDB list link";
export const LINK_CHANGES_AT_NEXT_SYNC = "Public lists only. Titles change at the next sync.";
export const TMDB_LIST_INVALID =
  "Paste the link to a public list on themoviedb.org, like https://www.themoviedb.org/list/310.";
export const PICK_A_CHART = "Pick a chart.";
export const CHART_SET_WHEN_MADE = "set when it was made";
export const FRANCHISE_ID_LABEL = "TMDB collection ID";
export const FIND_FRANCHISE_ID = "Find the ID on themoviedb.org";
export const FRANCHISE_ID_HELP =
  "The number in the collection's link, for example themoviedb.org/collection/119. Titles change at the next sync.";
export const FRANCHISE_ID_MISSING =
  "This list doesn't follow a TMDB collection yet. Add its ID so it can sync.";
export const FRANCHISE_ID_INVALID = "Use the number from the collection's link, like 119.";
export const DISCOVER_LOCKED = "Made by a starter pack. Its rules can't be changed here.";
export const DISCOVER_STILL_EDITABLE =
  "You can still change its name, artwork, max titles, schedule and where it shows.";
export const TRAKT_LOCKED =
  "New Trakt lists aren't supported. This one keeps its source and libraries.";
export const TRAKT_STILL_EDITABLE =
  "You can still change its name, artwork, order, schedule and where it shows.";
export const TRAKT_SCHEDULE_STOPPED = "A stopped Trakt list can't be scheduled again.";
export const PERSONAL_SCHEDULE_LOCKED =
  "This server doesn't let profiles change a list's schedule.";
export const LIST_DECIDES = "The list decides";
export const LIST_DECIDES_ITEMS = [
  "Which titles are in it",
  "Their order, while the sort is “List order”",
] as const;
export const YOU_DECIDE = "You decide";

/** What the editor still controls on a list-backed collection. */
export function youDecideItems(scheduleEditable: boolean): string[] {
  return [
    "Name, description and artwork",
    scheduleEditable ? "Order, max titles and the schedule" : "Order and max titles",
    "Where it shows",
  ];
}

/** "3 hours ago": how long ago a sync ran. */
export function syncedAgo(iso: string, now = Date.now()): string {
  const seconds = Math.round((now - Date.parse(iso)) / 1000);
  if (Number.isNaN(seconds)) return "recently";
  if (seconds < 60) return "just now";
  const steps: Array<[number, string]> = [
    [60, "minute"],
    [24, "hour"],
    [30, "day"],
    [12, "month"],
  ];
  let value = Math.round(seconds / 60);
  for (const [size, unit] of steps) {
    if (value < size) return `${plural(value, unit)} ago`;
    value = Math.round(value / size);
  }
  return `${plural(value, "year")} ago`;
}

/** "41 titles skipped". */
export function titlesSkipped(count: number): string {
  return `${plural(count, "title")} skipped`;
}

/** Why a synced list skips titles, and what brings them in. */
export function skippedExplanation(
  count: number | undefined,
  libraryNames: readonly string[],
  canSync = true,
) {
  const where = libraryNames.length > 0 ? joinNames(libraryNames) : "your libraries";
  if (count === undefined) {
    const skipped = `Titles on the list that aren't in ${where} are skipped.`;
    return canSync ? `${skipped} Sync now to count them.` : skipped;
  }
  const verb = count === 1 ? "isn't" : "aren't";
  return `${plural(count, "title")} on the list ${verb} in ${where}, so they're skipped. Add them to one of those libraries and they join at the next sync.`;
}

/** The red callout at the top of a list whose last sync failed. */
export function syncFailedLead(lastAt: string | undefined): string {
  return lastAt ? `The last sync failed ${syncedAgo(lastAt)}.` : "The last sync failed.";
}

export function keepsTitles(count: number): string {
  return `The collection keeps its ${plural(count, "title")}.`;
}

// --- Save bar ---------------------------------------------------------------

/** How the save bar and the conflict banner name a draft field. */
export const DRAFT_FIELD_LABEL: Readonly<Record<DraftField, string>> = {
  name: "Name",
  description: "Description",
  libraryIds: "Libraries",
  rules: "Rules",
  rawSortConfig: "Order",
  showOnly: "Show only",
  visibility: SHOW_ON_TAB_LABEL,
  shared: SHOW_TO_OTHER_PROFILES_LABEL,
  inLibraryTabs: SHOW_ON_TAB_LABEL,
  list: "List",
  limit: "Max titles",
  schedule: "Sync schedule",
};

export const ARTWORK_SLOT_LABEL: Readonly<Record<ArtworkSlot, string>> = {
  poster: "Poster",
  backdrop: "Backdrop",
};

// --- Look (artwork) ---------------------------------------------------------

/**
 * What a slot will show after Save: a chosen image not saved yet, the saved
 * one, the collage the server made of the collection's titles, no collage yet
 * (none of its titles has a poster, or the collage is still being made), or
 * nothing at all (a backdrop, or a server Smart collection's poster).
 */
export type ArtworkState = "none" | "awaiting-collage" | "collage" | "new" | "saved";

const ARTWORK_STATE_TEXT: Readonly<Record<ArtworkSlot, Record<ArtworkState, string>>> = {
  poster: {
    none: "none",
    "awaiting-collage": "a collage once its titles have posters",
    collage: "a collage of its titles",
    new: "a new image",
    saved: "an image",
  },
  backdrop: {
    none: "none",
    "awaiting-collage": "none",
    collage: "none",
    new: "a new image",
    saved: "an image",
  },
};

/** "Poster: a collage of its titles · Backdrop: none", for the closed Look card. */
export function lookSummary(slots: ReadonlyArray<[ArtworkSlot, ArtworkState]>): string {
  return slots
    .map(([slot, state]) => `${ARTWORK_SLOT_LABEL[slot]}: ${ARTWORK_STATE_TEXT[slot][state]}`)
    .join(" · ");
}

/** Under the poster tile while it shows the collage. */
export const POSTER_IS_COLLAGE = "Collage of its titles";
/** Under the poster tile while there's no image and no collage yet. */
export const POSTER_AWAITS_COLLAGE = "A collage once its titles have posters";
/** Under the poster tile of a collection that never gets a collage. */
export const NO_POSTER = "No poster";
/** Under the backdrop tile: what the image is for. */
export const BACKDROP_CAPTION = "Fills the top of its page";

export const NOT_CREATED_YET = "Not created yet";
export const TITLES_ALREADY_SAVED = "Titles are already saved.";
export const SAVE_FAILED = "Couldn't save";

/** Why Create waits on a server collection with no library yet. */
export const PICK_LIBRARIES_FIRST = "Pick its libraries, then create it.";

/** Why Save waits on a saved server collection with every library unticked. */
export const PICK_A_LIBRARY = "Pick at least one library.";

export const NAME_IT_THEN_CREATE = "Name it, then create it.";

export function titlesReadyToAdd(count: number): string {
  return `${plural(count, "title")} ready to add`;
}

/** "Name and description not saved": the first field as labelled, the rest in lower case. */
export function notSavedMessage(fieldLabels: readonly string[]): string {
  const [first, ...rest] = fieldLabels;
  if (!first) return "";
  const names = [first, ...rest.map((label) => label.charAt(0).toLowerCase() + label.slice(1))];
  return `${joinNames(names)} not saved`;
}

// --- Conflicts and deletes --------------------------------------------------

export const CONFLICT_TITLE = "This collection changed since you opened it.";

export function serverDeleteDescription(libraryNames: readonly string[]): string {
  if (libraryNames.length === 0) return "It's removed for everyone. This can't be undone.";
  return `It's removed from ${joinNames(libraryNames)} for everyone. This can't be undone.`;
}

export function personalDeleteDescription(shared: boolean): string {
  return shared
    ? "It's removed for you and every profile you share it with. This can't be undone."
    : "This can't be undone.";
}

// --- Arrange ----------------------------------------------------------------

export function arrangeHeading(libraryName: string): string {
  return `Shelves on ${libraryName} › Collections`;
}
export const ARRANGE_SUBTITLE =
  "Shelves are set separately for each library. Top to bottom, the way viewers see them.";
export const ARRANGE_HINT =
  "Drag shelves and collections, or focus a handle and press Space, then the arrow keys. ⋯ has Move to shelf. Changes save right away.";

export const NO_HEADING = "No heading";
export const NO_HEADING_HELP = "Collections not on a shelf, shown without a title";
export const MY_COLLECTIONS_TAG = "Different for each viewer";
export const MY_COLLECTIONS_NOTE = `Each viewer's own collections land here when they turn on “${SHOW_ON_TAB_LABEL}”. You can rename or move this shelf.`;
/** Shown on My collections while a server collection is dragged. */
export const MY_COLLECTIONS_NO_DROP = "Viewers' own collections only";
export const HIDDEN_TAG = "Hidden";
export const MOVE_FAILED = "Couldn't move it";
/** A move found the order changed by someone else since Arrange read it; nothing was saved. */
export const ORDER_CHANGED =
  "Someone else changed this order, so nothing moved. Arrange now shows their order; move it again.";

export const VIEWER_PREVIEW_LABEL = "What viewers see";
export const VIEWER_PREVIEW_MINE = "Each viewer's own";
export const VIEWER_PREVIEW_NOTE =
  "The pin marks a collection kept at the start of its shelf. Hidden collections don't appear.";

// --- Pin (`featured`) ---------------------------------------------------------

export const PIN_LABEL = "Pin to the start of its shelf";
export const UNPIN_LABEL = "Unpin";
export const PINNED = "Pinned";
export const PINNED_BAND = "Pinned to the start";

/** Pin is set on the collection, not per library, so it reaches every library the collection is in. */
const PIN_EVERY_LIBRARY = " This applies in every library it's in.";

/**
 * What Pin does, given what the shelf sorts by (null for Your order) and
 * whether the collection is in more than one library. Pinned collections also
 * lead the capped Server collections list on every profile's Collections
 * page, which is all Pin does on a shelf that sorts itself.
 */
export function pinHelp(shelfSortedBy: string | null, inSeveralLibraries = false): string {
  const help =
    shelfSortedBy === null
      ? "Shows first on this shelf and in Server collections on the Collections page."
      : `Shows first in Server collections on the Collections page; this shelf sorts by ${shelfSortedBy}.`;
  return inSeveralLibraries ? help + PIN_EVERY_LIBRARY : help;
}

export function unpinHelp(shelfSortedBy: string | null, inSeveralLibraries = false): string {
  const help =
    shelfSortedBy === null
      ? "Stops showing first on this shelf and in Server collections on the Collections page."
      : "Stops showing first in Server collections on the Collections page.";
  return inSeveralLibraries ? help + PIN_EVERY_LIBRARY : help;
}

/**
 * The phone sheet's Pin switch, which names the collection and its shelf
 * (null for No heading, which viewers never see as a name).
 */
export function pinSwitchLabel(name: string, shelfName: string | null): string {
  return `Pin ${name} to the start of ${shelfName ?? "the collections with no heading"}`;
}

export function deleteShelfTitle(name: string): string {
  return `Delete the ${name} shelf?`;
}

/** Deleting a shelf never deletes its collections, and touches one library only. */
export function deleteShelfDescription(collectionCount: number, libraryName: string): string {
  const members =
    collectionCount === 0
      ? "It has no collections."
      : `${collectionCount === 1 ? "Its 1 collection moves" : `Its ${collectionCount} collections move`} to ${NO_HEADING} on ${libraryName} › Collections. ${collectionCount === 1 ? "It isn't" : "They aren't"} deleted.`;
  return `${members} Only ${libraryName} changes; shelves in other libraries stay as they are. You can make the shelf again later.`;
}
