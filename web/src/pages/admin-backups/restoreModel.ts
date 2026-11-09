import { t, type MessageKey, type TextValues } from "@/i18n";
import { appLanguage } from "@/lib/appLocale";
import { DATA_TYPE_IDS, DATA_TYPE_PHRASES, pendingCount } from "@/lib/profileDataTypes";
import type {
  RestoreDataTypeId,
  RestoreErrorCode,
  RestoreItem,
  RestorePolicyDifference,
  RestoreProfile,
  RestoreReason,
  RestoreReport,
  RestoreSectionId,
  RestoreSelection,
  RestoreSet,
} from "@/api/v2/adminBackupRestore";

/**
 * A plain sentence for every reason the server gives for leaving an item out
 * or not offering it. Typed over the contract's reasons, so a new reason fails
 * the type check until it has a sentence here.
 */
export const REASON_SENTENCES: Record<RestoreReason, MessageKey> = {
  same: "web.admin.backups.restore.reason.same",
  path_in_use: "web.admin.backups.restore.reason.pathInUse",
  folder_missing: "web.admin.backups.restore.reason.folderMissing",
  library_not_restorable: "web.admin.backups.restore.reason.libraryNotRestorable",
  library_not_mapped: "web.admin.backups.restore.reason.libraryNotMapped",
  ambiguous_name: "web.admin.backups.restore.reason.ambiguousName",
  needs_another_key: "web.admin.backups.restore.reason.needsAnotherKey",
  config_needs_another_key: "web.admin.backups.restore.reason.configNeedsAnotherKey",
  other_version: "web.admin.backups.restore.reason.otherVersion",
  item_not_on_this_server: "web.admin.backups.restore.reason.itemNotOnThisServer",
  file_missing: "web.admin.backups.restore.reason.fileMissing",
  not_in_backup: "web.admin.backups.restore.reason.notInBackup",
  changed_since_preview: "web.admin.backups.restore.reason.changedSincePreview",
  ambiguous_account: "web.admin.backups.restore.reason.ambiguousAccount",
  ambiguous_profile: "web.admin.backups.restore.reason.ambiguousProfile",
  policy_wider: "web.admin.backups.restore.reason.policyWider",
  group_not_on_this_server: "web.admin.backups.restore.reason.groupNotOnThisServer",
  login_taken: "web.admin.backups.restore.reason.loginTaken",
  profile_limit: "web.admin.backups.restore.reason.profileLimit",
  needs_primary_profile: "web.admin.backups.restore.reason.needsPrimaryProfile",
  user_store_unsupported: "web.admin.backups.restore.reason.userStoreUnsupported",
  template_not_available: "web.admin.backups.restore.reason.templateNotAvailable",
  collection_not_mapped: "web.admin.backups.restore.reason.collectionNotMapped",
  unreadable: "web.admin.backups.restore.reason.unreadable",
  files_missing_here: "web.admin.backups.restore.reason.filesMissingHere",
  needs_install: "web.admin.backups.restore.reason.needsInstall",
  kept_stricter: "web.admin.backups.restore.reason.keptStricter",
  unverified_backup: "web.admin.backups.restore.reason.unverifiedBackup",
  over_restore_budget: "web.admin.backups.restore.reason.overRestoreBudget",
};

export const SECTION_TITLES: Record<RestoreSectionId, MessageKey> = {
  libraries: "web.admin.backups.restore.section.libraries",
  plugins: "web.admin.backups.restore.section.plugins",
  settings: "web.admin.backups.restore.section.settings",
  home_sections: "web.admin.backups.restore.section.homeSections",
  users: "web.admin.backups.restore.section.users",
  collections: "web.admin.backups.restore.section.collections",
};

/** On unless the admin turns them off: data a person would miss. Preferences overwrite, so they start off. */
export const DEFAULT_DATA_TYPES: ReadonlySet<RestoreDataTypeId> = new Set([
  "watch_history",
  "ratings",
  "favorites",
  "collections",
  "dismissals",
]);

/** Why a new account can't be created: "This server's Kids group allows more streams at once than the backup did." */
export function policyDifferenceSentence(
  group: string | undefined,
  difference: RestorePolicyDifference,
): string {
  const values = { field: difference.field, here: difference.here, backup: difference.backup };
  return group
    ? t("web.admin.backups.restore.policy.group", { ...values, group })
    : t("web.admin.backups.restore.policy.default", values);
}

/**
 * Why a restore stopped or never started, for every code the report gives.
 * Typed over the contract's codes, like {@link REASON_SENTENCES}.
 */
export const REPORT_ERROR_SENTENCES: Record<RestoreErrorCode, MessageKey> = {
  restore_set_changed: "web.admin.backups.restore.reportError.restoreSetChanged",
  busy: "web.admin.backups.restore.reportError.busy",
  before_restore_backup_failed: "web.admin.backups.restore.reportError.beforeRestoreBackupFailed",
  interrupted: "web.admin.backups.restore.reportError.interrupted",
  failed: "web.admin.backups.restore.reportError.failed",
};

/**
 * The settings a restore never writes, as the preview lists them. An entry
 * ending in "." or "_" is a key prefix and reads "s3.*".
 */
export function neverRestoredSettings(entries: readonly string[]): string[] {
  return entries.map((entry) => (/[._]$/.test(entry) ? `${entry}*` : entry));
}

/** A message whose plural selects on `count`, which it prints as `count_text`. */
function counted(key: MessageKey, count: number, values: TextValues = {}): string {
  return t(key, { ...values, count, count_text: String(count) });
}

/** "a", "a and b", "a, b, and c" in English: the interface language's list. */
function listPhrase(parts: readonly string[]): string {
  return new Intl.ListFormat(appLanguage(), { style: "long", type: "conjunction" }).format(parts);
}

/**
 * A selection is a set of keys: `section:id` for items (the form `requires`
 * uses) and `users:<account>/profile:<profile>` for an account's profiles.
 */
export type SelectionKeys = ReadonlySet<string>;

export function itemKey(section: RestoreSectionId, id: string): string {
  return `${section}:${id}`;
}

export function profileKey(accountId: string, profileId: string): string {
  return `users:${accountId}/profile:${profileId}`;
}

export interface IndexedItem {
  type: "item";
  section: RestoreSectionId;
  item: RestoreItem;
  key: string;
}

/** A profile under an account; choosing it needs its account. */
export interface IndexedProfile {
  type: "profile";
  section: "users";
  account: RestoreItem;
  accountKey: string;
  profile: RestoreProfile;
  key: string;
}

export type IndexEntry = IndexedItem | IndexedProfile;

/** Every item and profile of a preview by its key, in restore order. */
export function indexItems(set: RestoreSet): Map<string, IndexEntry> {
  const index = new Map<string, IndexEntry>();
  for (const section of set.sections) {
    for (const item of section.items) {
      const key = itemKey(section.id, item.id);
      index.set(key, { type: "item", section: section.id, item, key });
      for (const profile of item.account?.profiles ?? []) {
        const pKey = profileKey(item.id, profile.id);
        index.set(pKey, {
          type: "profile",
          section: "users",
          account: item,
          accountKey: key,
          profile,
          key: pKey,
        });
      }
    }
  }
  return index;
}

export function entrySelectable(entry: IndexEntry | undefined): boolean {
  if (!entry) return false;
  return entry.type === "item"
    ? entry.item.selectable
    : entry.profile.selectable && entry.account.selectable;
}

function entryRequires(entry: IndexEntry): readonly string[] {
  return entry.type === "item" ? entry.item.requires : [entry.accountKey];
}

/**
 * Branding assets by setting key, named as Appearance settings names them
 * (internal/branding/assets.go holds the list).
 */
const BRANDING_NAMES: Record<string, MessageKey> = {
  "branding.wordmark_ref": "web.admin.backups.restore.branding.wordmark",
  "branding.wordmark_light_ref": "web.admin.backups.restore.branding.wordmarkLight",
  "branding.mark_ref": "web.admin.backups.restore.branding.mark",
  "branding.mark_light_ref": "web.admin.backups.restore.branding.markLight",
  "branding.favicon_ref": "web.admin.backups.restore.branding.favicon",
  "branding.login_bg_ref": "web.admin.backups.restore.branding.loginBackground",
};

/**
 * A branding setting key as a name: "branding.mark_ref" reads "logo (icon)".
 * A key Silo doesn't know yet loses its prefix and suffix: "branding.banner_ref"
 * reads "banner".
 */
function brandingName(key: string): string {
  const known = Object.hasOwn(BRANDING_NAMES, key) ? BRANDING_NAMES[key] : undefined;
  if (known) return t(known);
  return key
    .replace(/^branding\./, "")
    .replace(/_ref$/, "")
    .replaceAll("_", " ");
}

/** An item's name for display: branding items carry a setting key as their label. */
export function displayLabel(item: { kind: RestoreItem["kind"]; label: string }): string {
  return item.kind === "branding" ? capitalize(brandingName(item.label)) : item.label;
}

function itemName(entry: IndexEntry): string {
  if (entry.type === "profile") {
    return t("web.admin.backups.restore.itemName.profile", { name: entry.profile.name });
  }
  const name = entry.item.label;
  switch (entry.item.kind) {
    case "library":
      return t("web.admin.backups.restore.itemName.library", { name });
    case "settings_page":
      return t("web.admin.backups.restore.itemName.page", { name });
    case "collection":
      return t("web.admin.backups.restore.itemName.collection", { name });
    case "template_collection":
      return t("web.admin.backups.restore.itemName.template", { name });
    case "home_section":
      return t("web.admin.backups.restore.itemName.section", { name });
    case "branding":
      return t("web.admin.backups.restore.itemName.branding", { name: brandingName(name) });
    case "account":
      return t("web.admin.backups.restore.itemName.account", { name });
    default:
      return name;
  }
}

export interface SelectionChange {
  selection: Set<string>;
  /** What else changed, or why a choice could not be made. */
  notice: string | null;
}

/**
 * Chooses items and everything they require, transitively (a section's collection and that collection's library, a
 * profile's account). An item whose requirement cannot be chosen stays unchosen, and the notice says why.
 */
export function chooseItems(
  index: Map<string, IndexEntry>,
  selection: SelectionKeys,
  keys: readonly string[],
): SelectionChange {
  const next = new Set(selection);
  const added: IndexEntry[] = [];
  const blocked: string[] = [];
  for (const key of keys) {
    const entry = index.get(key);
    if (!entry || !entrySelectable(entry) || next.has(key)) continue;
    const { required, problem } = requirementsOf(index, entry);
    if (problem) {
      blocked.push(problem);
      continue;
    }
    next.add(key);
    for (const dependency of required) {
      if (!next.has(dependency.key)) {
        next.add(dependency.key);
        if (!keys.includes(dependency.key)) added.push(dependency);
      }
    }
  }
  const notices = [...blocked];
  if (added.length > 0) {
    notices.push(
      t("web.admin.backups.restore.notice.alsoChose", {
        count: added.length,
        names: joinNames(added.map(itemName)),
      }),
    );
  }
  return { selection: next, notice: notices.length > 0 ? notices.join(" ") : null };
}

/** Everything an entry requires, transitively, or why one of them can't be chosen. */
function requirementsOf(
  index: Map<string, IndexEntry>,
  entry: IndexEntry,
): { required: IndexEntry[]; problem: string | null } {
  const required: IndexEntry[] = [];
  const seen = new Set([entry.key]);
  const pending = [...entryRequires(entry)];
  // for...of visits what the loop appends to pending.
  for (const requiredKey of pending) {
    if (seen.has(requiredKey)) continue;
    seen.add(requiredKey);
    const dependency = index.get(requiredKey);
    if (!dependency) {
      return {
        required,
        problem: t("web.admin.backups.restore.notice.needsMissingLibrary", {
          item: capitalize(itemName(entry)),
        }),
      };
    }
    if (!entrySelectable(dependency)) {
      return {
        required,
        problem: t("web.admin.backups.restore.notice.needsBlocked", {
          item: capitalize(itemName(entry)),
          dependency: itemName(dependency),
        }),
      };
    }
    required.push(dependency);
    pending.push(...entryRequires(dependency));
  }
  return { required, problem: null };
}

/** Unchooses items and every chosen item that requires one of them. */
export function unchooseItems(
  index: Map<string, IndexEntry>,
  selection: SelectionKeys,
  keys: readonly string[],
): SelectionChange {
  const next = new Set(selection);
  for (const key of keys) next.delete(key);
  const dependents: IndexEntry[] = [];
  // Repeat until nothing changes: unchoosing a collection also unchooses the sections that show it.
  for (let changed = true; changed; ) {
    changed = false;
    for (const key of [...next]) {
      const entry = index.get(key);
      if (entry && entryRequires(entry).some((requiredKey) => !next.has(requiredKey))) {
        next.delete(key);
        changed = true;
        if (!keys.includes(key)) dependents.push(entry);
      }
    }
  }
  if (dependents.length === 0) return { selection: next, notice: null };
  const names =
    dependents.length <= 3
      ? joinNames(dependents.map(itemName))
      : counted("web.admin.backups.restore.notice.itemCount", dependents.length);
  return {
    selection: next,
    notice: t("web.admin.backups.restore.notice.alsoUnchose", { count: dependents.length, names }),
  };
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/**
 * Names as a list. English keeps its "a, b and c" (no serial comma); other
 * languages use their own list pattern.
 */
function joinNames(names: string[]): string {
  if (names.length <= 1) return names[0] ?? "";
  if (appLanguage() !== "en") {
    return new Intl.ListFormat(appLanguage(), { style: "long", type: "conjunction" }).format(names);
  }
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`;
}

/** The keys an item's checkbox toggles: the item and, for an account, its choosable profiles. */
export function toggleKeys(section: RestoreSectionId, item: RestoreItem): string[] {
  const key = itemKey(section, item.id);
  const profiles = (item.account?.profiles ?? [])
    .filter((profile) => profile.selectable)
    .map((profile) => profileKey(item.id, profile.id));
  return [key, ...profiles];
}

/** The chosen items (not profiles), in restore order. */
export function chosenItems(set: RestoreSet, selection: SelectionKeys): IndexedItem[] {
  return [...indexItems(set).values()].filter(
    (entry): entry is IndexedItem => entry.type === "item" && selection.has(entry.key),
  );
}

/**
 * The profile a new account must restore with any of its others: the
 * backup's primary profile. Only a primary profile manages a household, and a
 * restore never makes another profile primary, so the server refuses a new
 * account whose chosen profiles leave it out.
 */
export function requiredPrimary(account: RestoreItem): RestoreProfile | undefined {
  if (account.account?.target !== "new") return undefined;
  return account.account.profiles.find((profile) => profile.is_primary && profile.selectable);
}

/** An account's chosen profiles, its required primary profile with any others. */
export function chosenProfiles(account: RestoreItem, selection: SelectionKeys): RestoreProfile[] {
  const primary = requiredPrimary(account);
  const chosen = (account.account?.profiles ?? []).filter(
    (profile) => profile.selectable && selection.has(profileKey(account.id, profile.id)),
  );
  if (primary && chosen.length > 0 && !chosen.includes(primary)) {
    return [primary, ...chosen];
  }
  return chosen;
}

/** Whether any chosen profile carries at least one of the chosen data types. */
function chosenDataTypes(
  accounts: readonly IndexedItem[],
  selection: SelectionKeys,
  dataTypes: ReadonlySet<RestoreDataTypeId>,
): RestoreDataTypeId[] {
  const carried = new Set<RestoreDataTypeId>();
  for (const entry of accounts) {
    for (const profile of chosenProfiles(entry.item, selection)) {
      for (const data of profile.data_types) if (pendingCount(data) > 0) carried.add(data.id);
    }
  }
  return DATA_TYPE_IDS.filter((id) => dataTypes.has(id) && carried.has(id));
}

/**
 * The restore request's `selection` for the chosen keys. Accounts and
 * profiles carry the targets the preview showed, and each profile the chosen
 * data types it has.
 */
export function selectionRequest(
  set: RestoreSet,
  selection: SelectionKeys,
  dataTypes: ReadonlySet<RestoreDataTypeId>,
): RestoreSelection {
  const request: Required<RestoreSelection> = {
    libraries: [],
    plugins: [],
    settings: [],
    users: [],
    collections: [],
    home_sections: [],
  };
  for (const entry of chosenItems(set, selection)) {
    if (entry.section === "users") {
      request.users.push({
        account: entry.item.id,
        target: entry.item.account?.target ?? "new",
        profiles: chosenProfiles(entry.item, selection).map((profile) => ({
          profile: profile.id,
          target: profile.target,
          data_types: DATA_TYPE_IDS.filter(
            (id) => dataTypes.has(id) && profile.data_types.some((data) => data.id === id),
          ),
        })),
      });
    } else {
      request[entry.section].push(entry.item.id);
    }
  }
  return request;
}

/** Settings on a page that a restore would change. */
export function differingSettings(item: RestoreItem) {
  return (item.settings ?? []).filter((setting) => setting.status === "differs");
}

/** What restoring one item does, as a sentence for "Silo will". */
function itemSentence(entry: IndexedItem): string {
  const { item } = entry;
  const name = item.label;
  switch (item.kind) {
    case "library":
      return item.status === "not_on_this_server"
        ? t("web.admin.backups.restore.plan.libraryAdd", { name })
        : t("web.admin.backups.restore.plan.libraryUpdate", { name });
    case "plugin": {
      const plugin = item.plugin;
      if (plugin?.installed_version) {
        return plugin.installed_version === plugin.version
          ? t("web.admin.backups.restore.plan.pluginSettings", { name })
          : t("web.admin.backups.restore.plan.pluginSettingsOtherVersion", {
              name,
              version: plugin.installed_version,
            });
      }
      if (plugin && !plugin.config_restorable)
        return t("web.admin.backups.restore.plan.pluginInstallOff", { name });
      if (plugin && plugin.config_keys.length > 0)
        return t("web.admin.backups.restore.plan.pluginInstallWithSettings", { name });
      return t("web.admin.backups.restore.plan.pluginInstall", { name });
    }
    case "settings_page":
      return counted(
        item.external_service
          ? "web.admin.backups.restore.plan.settingsPageExternal"
          : "web.admin.backups.restore.plan.settingsPage",
        differingSettings(item).length,
        { name },
      );
    case "account":
      return item.account?.target === "new"
        ? t("web.admin.backups.restore.plan.accountCreate", { name })
        : t("web.admin.backups.restore.plan.accountMerge", { name });
    case "collection": {
      const collection = item.collection;
      if (item.status === "not_on_this_server") {
        return collection
          ? counted(
              "web.admin.backups.restore.plan.collectionCreateWithItems",
              collection.items - collection.unknown_items,
              { name },
            )
          : t("web.admin.backups.restore.plan.collectionCreate", { name });
      }
      return collection
        ? counted("web.admin.backups.restore.plan.collectionAddItems", collection.missing_items, {
            name,
          })
        : t("web.admin.backups.restore.plan.collectionAddMissing", { name });
    }
    case "template_collection":
      return item.collection?.local_id
        ? t("web.admin.backups.restore.plan.templateApply", { name })
        : t("web.admin.backups.restore.plan.templateRecreate", { name });
    case "home_section":
      return item.home_section?.local_id
        ? t("web.admin.backups.restore.plan.sectionReplace", { name })
        : t("web.admin.backups.restore.plan.sectionAdd", { name });
    case "item_artwork":
      return t("web.admin.backups.restore.plan.artwork", { name });
    case "branding":
      return t("web.admin.backups.restore.plan.branding", { name: brandingName(name) });
  }
}

/** The users section's sentences: accounts merged, accounts created, and the data restored. */
function usersSentences(
  accounts: readonly IndexedItem[],
  selection: SelectionKeys,
  dataTypes: ReadonlySet<RestoreDataTypeId>,
): string[] {
  const sentences: string[] = [];
  const profileCount = (group: readonly IndexedItem[]) =>
    group.reduce((sum, entry) => sum + chosenProfiles(entry.item, selection).length, 0);
  const merged = accounts.filter((entry) => entry.item.account?.target !== "new");
  const created = accounts.filter((entry) => entry.item.account?.target === "new");
  if (merged.length > 0) {
    const profiles = profileCount(merged);
    sentences.push(
      profiles > 0
        ? counted("web.admin.backups.restore.plan.mergeAccountsWithProfiles", merged.length, {
            profiles: counted("web.admin.backups.restore.plan.profileCount", profiles),
          })
        : counted("web.admin.backups.restore.plan.mergeAccounts", merged.length),
    );
  }
  if (created.length > 0) {
    const profiles = profileCount(created);
    sentences.push(
      profiles > 0
        ? counted("web.admin.backups.restore.plan.createAccountsWithProfiles", created.length, {
            profiles: counted("web.admin.backups.restore.plan.profileCount", profiles),
          })
        : counted("web.admin.backups.restore.plan.createAccounts", created.length),
    );
  }
  if (profileCount(accounts) > 0) {
    const types = chosenDataTypes(accounts, selection, dataTypes);
    sentences.push(
      types.length > 0
        ? t("web.admin.backups.restore.plan.restoreData", {
            data: listPhrase(types.map((id) => DATA_TYPE_PHRASES[id])),
          })
        : dataTypes.size > 0
          ? t("web.admin.backups.restore.plan.nothingToMerge")
          : t("web.admin.backups.restore.plan.profilesWithoutData"),
    );
  }
  return sentences;
}

/**
 * Starter-pack collections count up into one or two sentences; every other
 * item gets its own.
 */
function itemSentences(entries: readonly IndexedItem[]): string[] {
  const templates = entries.filter((entry) => entry.item.kind === "template_collection");
  const recreated = templates.filter((entry) => !entry.item.collection?.local_id).length;
  const updated = templates.length - recreated;
  const sentences = entries
    .filter((entry) => entry.item.kind !== "template_collection")
    .map((entry) => itemSentence(entry));
  if (recreated > 0) {
    sentences.push(counted("web.admin.backups.restore.plan.templatesRecreate", recreated));
  }
  if (updated > 0) {
    sentences.push(counted("web.admin.backups.restore.plan.templatesApply", updated));
  }
  return sentences;
}

/** "Restore 3 home sections", or the pages they're on when mixed. */
function homeSectionSentences(entries: readonly IndexedItem[]): string[] {
  if (entries.length === 0) return [];
  const scopes = new Set(entries.map((entry) => entry.item.home_section?.scope ?? "home"));
  const count = entries.length;
  if (scopes.size === 1 && scopes.has("home"))
    return [counted("web.admin.backups.restore.plan.homeSections", count)];
  if (scopes.size === 1)
    return [counted("web.admin.backups.restore.plan.libraryPageSections", count)];
  return [counted("web.admin.backups.restore.plan.mixedSections", count)];
}

export interface PlanSection {
  section: RestoreSectionId;
  title: string;
  sentences: string[];
}

/** The chosen items as sentences, grouped by section in restore order. */
export function restorePlan(
  set: RestoreSet,
  selection: SelectionKeys,
  dataTypes: ReadonlySet<RestoreDataTypeId>,
): PlanSection[] {
  const chosen = chosenItems(set, selection);
  return set.sections
    .map((section) => {
      const inSection = chosen.filter((entry) => entry.section === section.id);
      return {
        section: section.id,
        title: t(SECTION_TITLES[section.id]),
        sentences:
          section.id === "users"
            ? usersSentences(inSection, selection, dataTypes)
            : section.id === "home_sections"
              ? homeSectionSentences(inSection)
              : itemSentences(inSection),
      };
    })
    .filter((group) => group.sentences.length > 0);
}

export interface Caveat {
  key: string;
  label: string;
  text: string;
}

function libraryAccessDropped(count: number): string {
  return counted("web.admin.backups.restore.caveat.librariesDropped", count);
}

/** What a chosen item leaves out or needs afterwards, for review's "Left out" list. */
export function restoreCaveats(set: RestoreSet, selection: SelectionKeys): Caveat[] {
  const caveats: Caveat[] = [];
  for (const entry of chosenItems(set, selection)) {
    const { item } = entry;
    const add = (suffix: string, text: string, label = displayLabel(item)) =>
      caveats.push({ key: `${entry.key}:${suffix}`, label, text });
    if (item.reason === "other_version" || item.reason === "config_needs_another_key") {
      add("reason", t(REASON_SENTENCES[item.reason]));
    }
    if (item.missing_blobs.length > 0) {
      add(
        "files",
        counted("web.admin.backups.restore.caveat.filesMissing", item.missing_blobs.length),
      );
    }
    const otherKey = (item.settings ?? []).filter(
      (setting) => setting.status === "needs_another_key",
    ).length;
    if (otherKey > 0) {
      add("key", counted("web.admin.backups.restore.caveat.otherKeySettings", otherKey));
    }
    const stricter = (item.settings ?? []).filter(
      (setting) => setting.status === "kept_stricter",
    ).length;
    const cleared = (item.settings ?? []).filter((setting) => setting.status === "cleared").length;
    if (cleared > 0) {
      add("cleared", t("web.admin.backups.restore.caveat.credentialsCleared"));
    }
    const unverified = (item.settings ?? []).filter(
      (setting) => setting.status === "unverified_backup",
    ).length;
    if (unverified > 0) {
      add("unverified", counted("web.admin.backups.restore.caveat.unverifiedSettings", unverified));
    }
    if (stricter > 0) {
      add("stricter", counted("web.admin.backups.restore.caveat.stricterSettings", stricter));
    }
    const unknown = item.collection?.unknown_items ?? 0;
    if (unknown > 0) {
      add("unknown", counted("web.admin.backups.restore.caveat.unknownItems", unknown));
    }
    if (item.restart_required) {
      const keys = differingSettings(item)
        .filter((setting) => setting.restart_required)
        .map((setting) => setting.key);
      add(
        "restart",
        keys.length > 0
          ? t("web.admin.backups.restore.caveat.restartKeys", { keys: keys.join(", ") })
          : t("web.admin.backups.restore.caveat.restart"),
      );
    }
    const account = item.account;
    if (account) {
      if (account.admin && account.target === "new") {
        add("admin", t("web.admin.backups.restore.caveat.administrator"));
      }
      if (account.libraries_left_out > 0) {
        add("account-libraries", libraryAccessDropped(account.libraries_left_out));
      }
      const profiles = chosenProfiles(item, selection);
      const extra =
        profiles.filter((profile) => profile.target === "new").length - account.profile_room;
      if (extra > 0) {
        add(
          "room",
          counted("web.admin.backups.restore.caveat.noRoom", extra, {
            room: counted("web.admin.backups.restore.caveat.roomCount", account.profile_room),
          }),
        );
      }
      for (const profile of profiles) {
        const label = t("web.admin.backups.restore.caveat.profileLabel", {
          account: item.label,
          profile: profile.name,
        });
        if (profile.no_library_access) {
          add(
            `profile:${profile.id}:access`,
            t("web.admin.backups.restore.caveat.noLibraryAccess"),
            label,
          );
        } else if (profile.libraries_left_out > 0) {
          add(
            `profile:${profile.id}:libraries`,
            libraryAccessDropped(profile.libraries_left_out),
            label,
          );
        }
      }
    }
  }
  return caveats;
}

/** The report's one-line result: "Restored 2 items, 1 left out, nothing failed." */
export function reportHeadline(counts: RestoreReport["counts"]): string {
  const parts = [
    counts.restored > 0
      ? counted("web.admin.backups.restore.report.restored", counts.restored)
      : t("web.admin.backups.restore.report.restoredNothing"),
  ];
  if (counts.unchanged > 0)
    parts.push(counted("web.admin.backups.restore.report.unchanged", counts.unchanged));
  if (counts.left_out > 0)
    parts.push(counted("web.admin.backups.restore.report.leftOut", counts.left_out));
  parts.push(
    counts.failed > 0
      ? counted("web.admin.backups.restore.report.failed", counts.failed)
      : t("web.admin.backups.restore.report.nothingFailed"),
  );
  return t("web.admin.backups.restore.report.headline", { parts: parts.join(", ") });
}
