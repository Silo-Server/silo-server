import type {
  RestoreItem,
  RestoreReason,
  RestoreSection,
  RestoreSet,
} from "@/api/v2/adminBackupRestore";
import { t, type MessageKey } from "@/i18n";

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

const SHORT: Record<ItemState, MessageKey> = {
  differs: "web.admin.backups.restore.counts.differsShort",
  new: "web.admin.backups.restore.counts.newShort",
  blocked: "web.admin.backups.restore.counts.blockedShort",
  same: "web.admin.backups.restore.counts.sameShort",
};

const LONG: Record<ItemState, MessageKey> = {
  differs: "web.admin.backups.restore.counts.differsLong",
  new: "web.admin.backups.restore.counts.newLong",
  blocked: "web.admin.backups.restore.counts.blockedLong",
  same: "web.admin.backups.restore.counts.sameLong",
};

function countText(key: MessageKey, count: number): string {
  return t(key, { count, count_text: count });
}

/** The count line beside the bar: "120 match · 52 differ · 12 new · 3 can't". */
export function stateCountsText(counts: StateCounts): string {
  const order: ItemState[] = ["same", "differs", "new", "blocked"];
  return order
    .filter((state) => counts[state] > 0)
    .map((state) => countText(SHORT[state], counts[state]))
    .join(" · ");
}

/** The bar's accessible name, in words. */
export function stateCountsLabel(counts: StateCounts): string {
  const parts = STATE_ORDER.filter((state) => counts[state] > 0).map((state) =>
    countText(LONG[state], counts[state]),
  );
  return parts.length > 0 ? parts.join(", ") : t("web.admin.backups.restore.counts.empty");
}

/** The one line a section with nothing to restore shows. */
export function nothingToRestoreText(counts: StateCounts): string {
  if (counts.blocked === 0) {
    return countText("web.admin.backups.restore.nothing.allMatch", counts.same);
  }
  const blocked = countText(LONG.blocked, counts.blocked);
  return counts.same > 0
    ? t("web.admin.backups.restore.nothing.sameAndBlocked", {
        same: countText(LONG.same, counts.same),
        blocked,
      })
    : t("web.admin.backups.restore.nothing.blocked", { blocked });
}

/**
 * Why a group of items can't be restored, as the group's heading: "8 can't be
 * restored — …". Typed over the contract's reasons, like REASON_SENTENCES.
 */
const BLOCKED_TITLES: Record<RestoreReason, MessageKey> = {
  same: "web.admin.backups.restore.blocked.same",
  path_in_use: "web.admin.backups.restore.blocked.pathInUse",
  folder_missing: "web.admin.backups.restore.blocked.folderMissing",
  library_not_restorable: "web.admin.backups.restore.blocked.libraryNotRestorable",
  library_not_mapped: "web.admin.backups.restore.blocked.libraryNotMapped",
  ambiguous_name: "web.admin.backups.restore.blocked.ambiguousName",
  needs_another_key: "web.admin.backups.restore.blocked.needsAnotherKey",
  config_needs_another_key: "web.admin.backups.restore.blocked.configNeedsAnotherKey",
  other_version: "web.admin.backups.restore.blocked.otherVersion",
  item_not_on_this_server: "web.admin.backups.restore.blocked.itemNotOnThisServer",
  file_missing: "web.admin.backups.restore.blocked.fileMissing",
  not_in_backup: "web.admin.backups.restore.blocked.notInBackup",
  changed_since_preview: "web.admin.backups.restore.blocked.changedSincePreview",
  ambiguous_account: "web.admin.backups.restore.blocked.ambiguousAccount",
  ambiguous_profile: "web.admin.backups.restore.blocked.ambiguousProfile",
  policy_wider: "web.admin.backups.restore.blocked.policyWider",
  group_not_on_this_server: "web.admin.backups.restore.blocked.groupNotOnThisServer",
  login_taken: "web.admin.backups.restore.blocked.loginTaken",
  profile_limit: "web.admin.backups.restore.blocked.profileLimit",
  user_store_unsupported: "web.admin.backups.restore.blocked.userStoreUnsupported",
  template_not_available: "web.admin.backups.restore.blocked.templateNotAvailable",
  collection_not_mapped: "web.admin.backups.restore.blocked.collectionNotMapped",
  unreadable: "web.admin.backups.restore.blocked.unreadable",
  files_missing_here: "web.admin.backups.restore.blocked.filesMissingHere",
  needs_install: "web.admin.backups.restore.blocked.needsInstall",
  kept_stricter: "web.admin.backups.restore.blocked.keptStricter",
  unverified_backup: "web.admin.backups.restore.blocked.unverifiedBackup",
  over_restore_budget: "web.admin.backups.restore.blocked.overRestoreBudget",
  needs_primary_profile: "web.admin.backups.restore.blocked.needsPrimaryProfile",
};

/** For an item the server gave a status but no reason. */
const BLOCKED_STATUS_TITLES: Partial<Record<RestoreItem["status"], MessageKey>> = {
  folder_missing: BLOCKED_TITLES.folder_missing,
  ambiguous: "web.admin.backups.restore.blocked.ambiguous",
  needs_another_key: BLOCKED_TITLES.needs_another_key,
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
  const blocked = new Map<string, { title: MessageKey; items: RestoreItem[] }>();
  for (const item of items) {
    const state = itemState(item);
    if (state === "same") same.push(item);
    else if (state !== "blocked") restorable.push(item);
    else {
      const key = item.reason ?? `status:${item.status}`;
      const title =
        (item.reason ? BLOCKED_TITLES[item.reason] : BLOCKED_STATUS_TITLES[item.status]) ??
        "web.admin.backups.restore.blocked.fallback";
      const group = blocked.get(key);
      if (group) group.items.push(item);
      else blocked.set(key, { title, items: [item] });
    }
  }
  return {
    restorable,
    same,
    blocked: [...blocked.entries()]
      .map(([key, group]) => ({
        key,
        title: countText(group.title, group.items.length),
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

const HOME_KEY = "home";
const ARTWORK_KEY = "artwork";
const NO_LIBRARY_KEY = "none";

/**
 * The library a collection or section belongs to: one mapped here, else the
 * backup's library it requires, else Home or the artwork group.
 */
function itemLibrary(item: RestoreItem, names: LibraryNames): LibraryRef {
  if (item.kind === "item_artwork" || item.kind === "branding") {
    return { key: ARTWORK_KEY, name: t("web.admin.backups.restore.library.artwork") };
  }
  if (item.home_section?.scope === "home") {
    return { key: HOME_KEY, name: t("web.admin.backups.restore.library.home") };
  }
  const localId = item.home_section?.library_id ?? item.collection?.library_ids?.[0];
  if (localId) {
    return {
      key: `local:${localId}`,
      name:
        names.local.get(localId) ??
        t("web.admin.backups.restore.library.fallback", { id: localId }),
    };
  }
  const required = item.requires.find((key) => key.startsWith("libraries:"));
  if (required) {
    const backupId = required.slice("libraries:".length);
    return {
      key: `backup:${backupId}`,
      name:
        names.backup.get(backupId) ??
        t("web.admin.backups.restore.library.fallback", { id: backupId }),
    };
  }
  return { key: NO_LIBRARY_KEY, name: t("web.admin.backups.restore.library.none") };
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
    group.key === HOME_KEY ? 0 : group.key === ARTWORK_KEY || group.key === NO_LIBRARY_KEY ? 2 : 1;
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
  if (restorable.length === 0) return t("web.admin.backups.restore.progress.nothing");
  const chosen = restorable.filter((item) => selection.has(itemKey(section.id, item.id))).length;
  return t("web.admin.backups.restore.progress.chosen", {
    chosen,
    chosen_text: chosen,
    total_text: restorable.length,
  });
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
      if (itemState(item) === "same") {
        return t("web.admin.backups.restore.row.sameFolders", { name: local ?? item.label });
      }
      return item.library?.paths.join(", ") ?? null;
    }
    case "plugin":
      return item.plugin?.installed_version
        ? t("web.admin.backups.restore.row.pluginInstalled", {
            version: item.plugin.installed_version,
          })
        : t("web.admin.backups.restore.row.pluginNotInstalled");
    case "settings_page": {
      const changed = differingSettings(item).length;
      return changed > 0
        ? countText("web.admin.backups.restore.row.settingsDiffer", changed)
        : null;
    }
    case "template_collection":
      return join(
        library,
        item.collection?.local_id
          ? t("web.admin.backups.restore.row.templateChanges")
          : t("web.admin.backups.restore.row.templateCreated"),
      );
    case "collection": {
      const collection = item.collection;
      if (!collection) return library;
      return join(
        library,
        item.status === "differs" && collection.missing_items > 0
          ? countText("web.admin.backups.restore.row.collectionAdds", collection.missing_items)
          : countText("web.admin.backups.restore.row.collectionItems", collection.items),
      );
    }
    case "home_section":
      return join(
        library,
        item.home_section ? sectionTypeName(item.home_section.section_type) : null,
        item.home_section?.local_id
          ? t("web.admin.backups.restore.row.sectionReplaces")
          : t("web.admin.backups.restore.row.sectionAdded"),
      );
    default:
      return library;
  }
}
