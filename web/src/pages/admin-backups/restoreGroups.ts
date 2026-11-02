import type {
  RestoreItem,
  RestoreReason,
  RestoreSection,
  RestoreSet,
} from "@/api/v2/adminBackupRestore";

import { differingSettings, itemKey, type SelectionKeys } from "./restoreModel";

/**
 * Where an item stands against this server: the same here, different, not
 * here yet, or not restorable for a reason.
 */
export type ItemState = "same" | "differs" | "new" | "blocked";

export function itemState(item: RestoreItem): ItemState {
  if (item.selectable) return item.status === "not_on_this_server" ? "new" : "differs";
  return item.status === "on_this_server" || item.reason === "same" ? "same" : "blocked";
}

export type StateCounts = Record<ItemState, number>;

export function countStates(items: readonly RestoreItem[]): StateCounts {
  const counts: StateCounts = { same: 0, differs: 0, new: 0, blocked: 0 };
  for (const item of items) counts[itemState(item)] += 1;
  return counts;
}

/** The bar's order: what can be restored first, what matches last. */
export const STATE_ORDER: readonly ItemState[] = ["differs", "new", "blocked", "same"];

const SHORT: Record<ItemState, (count: number) => string> = {
  differs: (count) => `${count} ${count === 1 ? "differs" : "differ"}`,
  new: (count) => `${count} new`,
  blocked: (count) => `${count} can't`,
  same: (count) => `${count} ${count === 1 ? "matches" : "match"}`,
};

const LONG: Record<ItemState, (count: number) => string> = {
  differs: (count) => `${count} ${count === 1 ? "differs" : "differ"} from this server`,
  new: (count) => `${count} ${count === 1 ? "is" : "are"} not on this server`,
  blocked: (count) => `${count} can't be restored`,
  same: (count) => `${count} ${count === 1 ? "matches" : "match"} this server`,
};

/** The count line beside the bar: "120 match · 52 differ · 12 new · 3 can't". */
export function stateCountsText(counts: StateCounts): string {
  const order: ItemState[] = ["same", "differs", "new", "blocked"];
  return order
    .filter((state) => counts[state] > 0)
    .map((state) => SHORT[state](counts[state]))
    .join(" · ");
}

/** The bar's accessible name, in words. */
export function stateCountsLabel(counts: StateCounts): string {
  const parts = STATE_ORDER.filter((state) => counts[state] > 0).map((state) =>
    LONG[state](counts[state]),
  );
  return parts.length > 0 ? parts.join(", ") : "Nothing in this backup";
}

/** The one line a section with nothing to restore shows. */
export function nothingToRestoreText(counts: StateCounts): string {
  if (counts.blocked === 0) {
    return counts.same === 1
      ? "It matches this server. Nothing to restore."
      : `All ${counts.same} match this server. Nothing to restore.`;
  }
  const parts = [
    counts.same > 0 ? LONG.same(counts.same) : null,
    LONG.blocked(counts.blocked),
  ].filter(Boolean);
  return `Nothing to restore: ${parts.join(" and ")}.`;
}

/**
 * Why a group of items can't be restored, as the end of "8 can't be restored
 * — …". Typed over the contract's reasons, like REASON_SENTENCES.
 */
const BLOCKED_PHRASES: Record<RestoreReason, string> = {
  same: "they're already the same here",
  path_in_use: "a folder belongs to another library here",
  folder_missing: "their folders aren't on this server",
  library_not_restorable: "their library can't be restored here",
  library_not_mapped: "their library isn't on this server",
  ambiguous_name: "more than one collection here has the name",
  needs_another_key: "they're encrypted with a different SECRET_KEY",
  config_needs_another_key: "their settings are encrypted with a different SECRET_KEY",
  other_version: "another version is installed here",
  item_not_on_this_server: "their title isn't in this server's catalog",
  file_missing: "the image was missing from the backup",
  not_in_backup: "the backup doesn't have them",
  changed_since_preview: "this server changed after the preview",
  ambiguous_account: "more than one account here matches",
  ambiguous_profile: "more than one profile here has the name",
  policy_wider: "this server would give them more access than the backup did",
  group_not_on_this_server: "their access group isn't on this server",
  login_taken: "another account here signs in with the username or email",
  profile_limit: "the account here has no room for another profile",
  user_store_unsupported: "accounts need the PostgreSQL user store",
  template_not_available: "this server's starter packs don't have the template",
  collection_not_mapped: "the collection they show isn't here or in the backup",
};

/** For an item the server gave a status but no reason. */
const BLOCKED_STATUS_PHRASES: Partial<Record<RestoreItem["status"], string>> = {
  folder_missing: BLOCKED_PHRASES.folder_missing,
  ambiguous: "more than one item here matches",
  needs_another_key: BLOCKED_PHRASES.needs_another_key,
};

export interface BlockedGroup {
  key: string;
  /** "8 can't be restored — their folders aren't on this server". */
  title: string;
  items: RestoreItem[];
}

export interface SectionGroups {
  /** Items that can be chosen, in the section's order. */
  restorable: RestoreItem[];
  /** Items that can't be chosen, one group per reason, largest first. */
  blocked: BlockedGroup[];
  /** Items already the same here. */
  same: RestoreItem[];
}

export function groupSectionItems(items: readonly RestoreItem[]): SectionGroups {
  const restorable: RestoreItem[] = [];
  const same: RestoreItem[] = [];
  const blocked = new Map<string, { phrase: string; items: RestoreItem[] }>();
  for (const item of items) {
    const state = itemState(item);
    if (state === "same") same.push(item);
    else if (state !== "blocked") restorable.push(item);
    else {
      const key = item.reason ?? `status:${item.status}`;
      const phrase =
        (item.reason ? BLOCKED_PHRASES[item.reason] : BLOCKED_STATUS_PHRASES[item.status]) ??
        "this server can't take them";
      const group = blocked.get(key);
      if (group) group.items.push(item);
      else blocked.set(key, { phrase, items: [item] });
    }
  }
  return {
    restorable,
    same,
    blocked: [...blocked.entries()]
      .map(([key, group]) => ({
        key,
        title: `${group.items.length} can't be restored — ${group.phrase}`,
        items: group.items,
      }))
      .sort((a, b) => b.items.length - a.items.length),
  };
}

/** Library names by this server's id and by the backup's id. */
export interface LibraryNames {
  local: ReadonlyMap<string, string>;
  backup: ReadonlyMap<string, string>;
}

export function libraryNames(
  set: Pick<RestoreSet, "library_map">,
  localLibraries: readonly { id: string | number; name: string }[],
): LibraryNames {
  const local = new Map(localLibraries.map((library) => [String(library.id), library.name]));
  const backup = new Map<string, string>();
  for (const mapping of set.library_map) {
    backup.set(mapping.backup_id, mapping.name);
    if (mapping.local_id && !local.has(mapping.local_id)) local.set(mapping.local_id, mapping.name);
  }
  return { local, backup };
}

export interface LibraryRef {
  key: string;
  name: string;
}

const HOME: LibraryRef = { key: "home", name: "Home" };
const ARTWORK: LibraryRef = { key: "artwork", name: "Artwork and branding" };
const NO_LIBRARY: LibraryRef = { key: "none", name: "No library" };

/**
 * The library a collection or section belongs to: one mapped here, else the
 * backup's library it requires, else Home or the artwork group.
 */
function itemLibrary(item: RestoreItem, names: LibraryNames): LibraryRef {
  if (item.kind === "item_artwork" || item.kind === "branding") return ARTWORK;
  if (item.home_section?.scope === "home") return HOME;
  const localId = item.home_section?.library_id ?? item.collection?.library_ids?.[0];
  if (localId) {
    return { key: `local:${localId}`, name: names.local.get(localId) ?? `Library ${localId}` };
  }
  const required = item.requires.find((key) => key.startsWith("libraries:"));
  if (required) {
    const backupId = required.slice("libraries:".length);
    return {
      key: `backup:${backupId}`,
      name: names.backup.get(backupId) ?? `Library ${backupId}`,
    };
  }
  return NO_LIBRARY;
}

export interface LibraryGroup extends LibraryRef {
  items: RestoreItem[];
}

/** Items by library: Home first, libraries by name, the artwork and no-library groups last. */
export function groupByLibrary(items: readonly RestoreItem[], names: LibraryNames): LibraryGroup[] {
  const groups = new Map<string, LibraryGroup>();
  for (const item of items) {
    const library = itemLibrary(item, names);
    const group = groups.get(library.key);
    if (group) group.items.push(item);
    else groups.set(library.key, { ...library, items: [item] });
  }
  const rank = (group: LibraryGroup) =>
    group.key === HOME.key ? 0 : group.key === ARTWORK.key || group.key === NO_LIBRARY.key ? 2 : 1;
  return [...groups.values()].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
}

/** Sections whose rows sit under library sub-headings. */
export function groupsByLibrary(section: RestoreSection["id"]): boolean {
  return section === "collections" || section === "home_sections";
}

/** Sections with more items than this show a filter. */
export const FILTER_THRESHOLD = 20;

/**
 * Whether an item matches a filter: its label, its library, an account's
 * username, email, or local name, or one of its profiles' names.
 */
export function matchesFilter(item: RestoreItem, query: string, names: LibraryNames): boolean {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  const account = item.account;
  const haystack = [
    item.label,
    item.collection || item.home_section ? itemLibrary(item, names).name : null,
    item.library?.local_id ? names.local.get(item.library.local_id) : null,
    account?.username,
    account?.email,
    account?.local_username,
    ...(account?.profiles.map((profile) => profile.name) ?? []),
  ];
  return haystack.some((value) => value?.toLowerCase().includes(needle));
}

/** "2 chosen of 12", or "nothing to restore", for the section index. */
export function sectionProgress(section: RestoreSection, selection: SelectionKeys): string {
  const restorable = section.items.filter((item) => item.selectable);
  if (restorable.length === 0) return "nothing to restore";
  const chosen = restorable.filter((item) => selection.has(itemKey(section.id, item.id))).length;
  return `${chosen} chosen of ${restorable.length}`;
}

function sectionTypeName(type: string): string {
  const name = type.replaceAll("_", " ");
  return name.charAt(0).toUpperCase() + name.slice(1);
}

/**
 * The short muted text after a row's label: what the item is here. With
 * `withLibrary`, collections and sections name their library too, for lists
 * that aren't already under a library heading.
 */
export function rowSecondary(
  item: RestoreItem,
  names: LibraryNames,
  withLibrary = false,
): string | null {
  const library =
    withLibrary && (item.collection || item.home_section) ? itemLibrary(item, names).name : null;
  const join = (...parts: (string | null | undefined)[]) =>
    parts.filter(Boolean).join(" · ") || null;
  switch (item.kind) {
    case "library": {
      const local = item.library?.local_id ? names.local.get(item.library.local_id) : undefined;
      if (itemState(item) === "same") return `Same folders as ${local ?? item.label}`;
      return item.library?.paths.join(", ") ?? null;
    }
    case "plugin":
      return item.plugin?.installed_version
        ? `${item.plugin.installed_version} installed here`
        : "Not installed here";
    case "settings_page": {
      const changed = differingSettings(item).length;
      return changed > 0
        ? `${changed} ${changed === 1 ? "setting differs" : "settings differ"}`
        : null;
    }
    case "template_collection":
      return join(
        library,
        item.collection?.local_id ? "your changes to it" : "created from the starter pack",
      );
    case "collection": {
      const collection = item.collection;
      if (!collection) return library;
      return join(
        library,
        item.status === "differs" && collection.missing_items > 0
          ? `adds ${collection.missing_items} ${collection.missing_items === 1 ? "item" : "items"}`
          : `${collection.items} ${collection.items === 1 ? "item" : "items"}`,
      );
    }
    case "home_section":
      return join(
        library,
        item.home_section ? sectionTypeName(item.home_section.section_type) : null,
        item.home_section?.local_id ? "replaces the section here" : "added to the page",
      );
    default:
      return library;
  }
}
