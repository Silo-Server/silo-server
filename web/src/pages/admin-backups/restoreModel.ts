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
export const REASON_SENTENCES: Record<RestoreReason, string> = {
  same: "It's already the same on this server.",
  path_in_use: "A folder of this library belongs to another library on this server.",
  folder_missing: "This server can't open one of the library's folders.",
  library_not_restorable: "Its library can't be restored on this server.",
  library_not_mapped: "Its library isn't on this server, and it wasn't chosen with it.",
  ambiguous_name:
    "More than one collection here has this name, so Silo can't tell which one to add to.",
  needs_another_key: "It was encrypted with a different SECRET_KEY, so this server can't read it.",
  config_needs_another_key:
    "Its settings were encrypted with a different SECRET_KEY, so it installs without them and stays off.",
  other_version: "Another version is installed here, so only its settings are restored.",
  item_not_on_this_server: "The title it belongs to isn't in this server's catalog.",
  file_missing: "Its image was missing when the backup was taken.",
  not_in_backup: "The backup doesn't have it.",
  changed_since_preview:
    "This server changed after the preview. Open the restore again to see it as it is now.",
  ambiguous_account:
    "More than one account here matches its username or email, so Silo can't tell which one it is.",
  ambiguous_profile:
    "More than one profile on the account here has this name, so Silo can't tell which one it is.",
  policy_wider: "This server would give it more access than it had in the backup.",
  group_not_on_this_server:
    "Its access group isn't on this server. Create a group with the same name, then open the restore again.",
  login_taken: "Another account here signs in with its username or email.",
  profile_limit: "The account here has no room for another profile.",
  user_store_unsupported: "Accounts can be restored only on the PostgreSQL user store.",
  template_not_available:
    "This server's starter packs don't have its template, or the template doesn't suit the library.",
  collection_not_mapped:
    "The collection it shows isn't on this server or in the backup, so there's nothing for it to show.",
};

export const SECTION_TITLES: Record<RestoreSectionId, string> = {
  libraries: "Libraries",
  plugins: "Plugins",
  settings: "Settings",
  home_sections: "Home and library sections",
  users: "Users",
  collections: "Collections and artwork",
};

/** On unless the admin turns them off: data a person would miss. Preferences overwrite, so they start off. */
export const DEFAULT_DATA_TYPES: ReadonlySet<RestoreDataTypeId> = new Set([
  "watch_history",
  "ratings",
  "favorites",
  "collections",
  "dismissals",
]);

const POLICY_FIELDS: Record<RestorePolicyDifference["field"], string> = {
  library_ids: "libraries",
  max_playback_quality: "playback quality",
  max_streams: "streams at once",
  max_transcodes: "transcodes at once",
  max_remote_stream_bitrate_kbps: "remote streaming bitrate",
  max_local_stream_bitrate_kbps: "local streaming bitrate",
  max_profiles: "profiles",
  download_allowed: "downloads",
  download_transcode_allowed: "converted downloads",
  transcode_allowed: "transcoding",
  audio_transcode_allowed: "audio transcoding",
  requests_allowed: "requests",
  permissions: "permissions",
};

/** Why a new account can't be created: "This server's Kids group allows more streams at once than the backup did." */
export function policyDifferenceSentence(
  group: string | undefined,
  difference: RestorePolicyDifference,
): string {
  const who = group ? `This server's ${group} group` : "This server's default access";
  return `${who} allows more ${POLICY_FIELDS[difference.field]} than the backup did (${difference.here} here, ${difference.backup} in the backup).`;
}

/**
 * Why a restore stopped or never started, for every code the report gives.
 * Typed over the contract's codes, like {@link REASON_SENTENCES}.
 */
export const REPORT_ERROR_SENTENCES: Record<RestoreErrorCode, string> = {
  restore_set_changed:
    "The backup changed after the preview, so nothing was restored. Open the restore again to see it as it is now.",
  busy: "Another backup or restore was running, so nothing was restored. Try again when it finishes.",
  before_restore_backup_failed:
    "Silo couldn't save a backup of this server first, so nothing was restored.",
  interrupted:
    "The restore stopped before it finished. Items above were committed; running the same restore again is safe.",
  failed:
    "The restore failed before it finished. Items above were committed; running the same restore again is safe.",
};

/**
 * The settings a restore never writes, as the preview lists them. An entry
 * ending in "." or "_" is a key prefix and reads "s3.*".
 */
export function neverRestoredSettings(entries: readonly string[]): string[] {
  return entries.map((entry) => (/[._]$/.test(entry) ? `${entry}*` : entry));
}

function plural(count: number, one: string, many = `${one}s`): string {
  return `${count} ${count === 1 ? one : many}`;
}

/** "a", "a and b", "a, b, and c". */
function listPhrase(parts: readonly string[]): string {
  if (parts.length <= 2) return parts.join(" and ");
  return `${parts.slice(0, -1).join(", ")}, and ${parts[parts.length - 1]}`;
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
const BRANDING_NAMES: Record<string, string> = {
  "branding.wordmark_ref": "logo (wordmark)",
  "branding.wordmark_light_ref": "logo (wordmark) for light themes",
  "branding.mark_ref": "logo (icon)",
  "branding.mark_light_ref": "logo (icon) for light themes",
  "branding.favicon_ref": "favicon",
  "branding.login_bg_ref": "login background",
};

/**
 * A branding setting key as a name: "branding.mark_ref" reads "logo (icon)".
 * A key Silo doesn't know yet loses its prefix and suffix: "branding.banner_ref"
 * reads "banner".
 */
function brandingName(key: string): string {
  return (
    BRANDING_NAMES[key] ??
    key
      .replace(/^branding\./, "")
      .replace(/_ref$/, "")
      .replaceAll("_", " ")
  );
}

/** An item's name for display: branding items carry a setting key as their label. */
export function displayLabel(item: { kind: RestoreItem["kind"]; label: string }): string {
  return item.kind === "branding" ? capitalize(brandingName(item.label)) : item.label;
}

function itemName(entry: IndexEntry): string {
  if (entry.type === "profile") return `the profile ${entry.profile.name}`;
  switch (entry.item.kind) {
    case "library":
      return `the ${entry.item.label} library`;
    case "settings_page":
      return `the ${entry.item.label} page`;
    case "collection":
      return `the ${entry.item.label} collection`;
    case "template_collection":
      return `the starter-pack collection ${entry.item.label}`;
    case "home_section":
      return `the section ${entry.item.label}`;
    case "branding":
      return `the branding ${brandingName(entry.item.label)}`;
    case "account":
      return `the account ${entry.item.label}`;
    default:
      return entry.item.label;
  }
}

export interface SelectionChange {
  selection: Set<string>;
  /** What else changed, or why a choice could not be made. */
  notice: string | null;
}

/**
 * Chooses items and what they require (a collection's library, a profile's
 * account). An item whose requirement cannot be chosen stays unchosen, and
 * the notice says why.
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
    const required = entryRequires(entry).map((requiredKey) => index.get(requiredKey));
    const missing = required.some((dependency) => dependency === undefined);
    const unavailable = required.find(
      (dependency) => dependency !== undefined && !entrySelectable(dependency),
    );
    if (missing || unavailable) {
      blocked.push(
        unavailable
          ? `${capitalize(itemName(entry))} needs ${itemName(unavailable)}, which can't be restored.`
          : `${capitalize(itemName(entry))} needs a library that isn't in this backup.`,
      );
      continue;
    }
    next.add(key);
    for (const dependency of required) {
      if (dependency && !next.has(dependency.key)) {
        next.add(dependency.key);
        if (!keys.includes(dependency.key)) added.push(dependency);
      }
    }
  }
  const notices = [...blocked];
  if (added.length > 0) {
    notices.push(
      `Also chose ${joinNames(added.map(itemName))}, which ${added.length === 1 ? "is" : "are"} needed by what you chose.`,
    );
  }
  return { selection: next, notice: notices.length > 0 ? notices.join(" ") : null };
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
  for (const key of [...next]) {
    const entry = index.get(key);
    if (entry && entryRequires(entry).some((requiredKey) => !next.has(requiredKey))) {
      next.delete(key);
      if (!keys.includes(key)) dependents.push(entry);
    }
  }
  if (dependents.length === 0) return { selection: next, notice: null };
  const names =
    dependents.length <= 3
      ? joinNames(dependents.map(itemName))
      : plural(dependents.length, "item");
  return {
    selection: next,
    notice: `Also unchose ${names}, which ${dependents.length === 1 ? "needs" : "need"} what you unchose.`,
  };
}

function capitalize(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

function joinNames(names: string[]): string {
  if (names.length <= 1) return names[0] ?? "";
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

/** An account's chosen profiles. */
export function chosenProfiles(account: RestoreItem, selection: SelectionKeys): RestoreProfile[] {
  return (account.account?.profiles ?? []).filter(
    (profile) => profile.selectable && selection.has(profileKey(account.id, profile.id)),
  );
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
  switch (item.kind) {
    case "library":
      return item.status === "not_on_this_server"
        ? `Add the ${item.label} library and scan it`
        : `Update ${item.label} to match the backup`;
    case "plugin": {
      const plugin = item.plugin;
      if (plugin?.installed_version) {
        return plugin.installed_version === plugin.version
          ? `Restore the settings of ${item.label}`
          : `Restore the settings of ${item.label} on the installed ${plugin.installed_version}`;
      }
      if (plugin && !plugin.config_restorable)
        return `Install ${item.label}, turned off and without its settings`;
      if (plugin && plugin.config_keys.length > 0) return `Install ${item.label} with its settings`;
      return `Install ${item.label}`;
    }
    case "settings_page": {
      const changed = differingSettings(item).length;
      const outside = item.external_service
        ? ", including some that connect to an outside service"
        : "";
      return `Replace ${plural(changed, "setting")} on the ${item.label} page${outside}`;
    }
    case "account":
      return item.account?.target === "new"
        ? `Create the account ${item.label}`
        : `Merge ${item.label} into the matching account here`;
    case "collection": {
      const collection = item.collection;
      if (item.status === "not_on_this_server") {
        return collection
          ? `Create the ${item.label} collection with ${plural(collection.items - collection.unknown_items, "item")}`
          : `Create the ${item.label} collection`;
      }
      return collection
        ? `Add ${plural(collection.missing_items, "item")} to the ${item.label} collection`
        : `Add the missing items to the ${item.label} collection`;
    }
    case "template_collection":
      return item.collection?.local_id
        ? `Apply your changes to the starter-pack collection ${item.label}`
        : `Recreate the starter-pack collection ${item.label} with its settings`;
    case "home_section":
      return item.home_section?.local_id
        ? `Replace the section ${item.label}`
        : `Add the section ${item.label}`;
    case "item_artwork":
      return `Restore the uploaded ${item.label}`;
    case "branding":
      return `Restore the branding ${brandingName(item.label)}`;
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
      `Merge ${plural(merged.length, "account")}${profiles > 0 ? ` (${plural(profiles, "profile")})` : ""} into the matching ${merged.length === 1 ? "account" : "accounts"} here`,
    );
  }
  if (created.length > 0) {
    const profiles = profileCount(created);
    sentences.push(
      `Create ${created.length} new ${created.length === 1 ? "account" : "accounts"}${profiles > 0 ? ` with ${plural(profiles, "profile")}` : ""}`,
    );
  }
  if (profileCount(accounts) > 0) {
    const types = chosenDataTypes(accounts, selection, dataTypes);
    sentences.push(
      types.length > 0
        ? `Restore their ${listPhrase(types.map((id) => DATA_TYPE_PHRASES[id]))}`
        : dataTypes.size > 0
          ? "Nothing to merge for the chosen data"
          : "Restore the chosen profiles without their data",
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
    sentences.push(
      `Recreate ${plural(recreated, "starter-pack collection")} and ${recreated === 1 ? "its" : "their"} settings`,
    );
  }
  if (updated > 0) {
    sentences.push(
      `Apply your changes to ${plural(updated, "starter-pack collection")} already here`,
    );
  }
  return sentences;
}

/** "Restore 3 home sections", or the pages they're on when mixed. */
function homeSectionSentences(entries: readonly IndexedItem[]): string[] {
  if (entries.length === 0) return [];
  const scopes = new Set(entries.map((entry) => entry.item.home_section?.scope ?? "home"));
  const count = entries.length;
  if (scopes.size === 1 && scopes.has("home")) return [`Restore ${plural(count, "home section")}`];
  if (scopes.size === 1) return [`Restore ${plural(count, "library page section")}`];
  return [`Restore ${plural(count, "section")} on Home and library pages`];
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
        title: SECTION_TITLES[section.id],
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
  return `${plural(count, "library", "libraries")} in its library access ${count === 1 ? "isn't" : "aren't"} on this server and ${count === 1 ? "is" : "are"} dropped. Nothing is widened.`;
}

/** What a chosen item leaves out or needs afterwards, for review's "Left out" list. */
export function restoreCaveats(set: RestoreSet, selection: SelectionKeys): Caveat[] {
  const caveats: Caveat[] = [];
  for (const entry of chosenItems(set, selection)) {
    const { item } = entry;
    const add = (suffix: string, text: string, label = displayLabel(item)) =>
      caveats.push({ key: `${entry.key}:${suffix}`, label, text });
    if (item.reason === "other_version" || item.reason === "config_needs_another_key") {
      add("reason", REASON_SENTENCES[item.reason]);
    }
    if (item.missing_blobs.length > 0) {
      add(
        "files",
        `${plural(item.missing_blobs.length, "file was", "files were")} missing when the backup was taken and ${item.missing_blobs.length === 1 ? "is" : "are"} left out.`,
      );
    }
    const otherKey = (item.settings ?? []).filter(
      (setting) => setting.status === "needs_another_key",
    ).length;
    if (otherKey > 0) {
      add(
        "key",
        `${plural(otherKey, "encrypted setting")} saved with a different SECRET_KEY ${otherKey === 1 ? "is" : "are"} left out.`,
      );
    }
    const unknown = item.collection?.unknown_items ?? 0;
    if (unknown > 0) {
      add(
        "unknown",
        `${plural(unknown, "item")} ${unknown === 1 ? "isn't" : "aren't"} in this server's catalog and ${unknown === 1 ? "is" : "are"} left out.`,
      );
    }
    if (item.restart_required) {
      const keys = differingSettings(item)
        .filter((setting) => setting.restart_required)
        .map((setting) => setting.key);
      add(
        "restart",
        keys.length > 0
          ? `Applies after a restart: ${keys.join(", ")}.`
          : "Applies after a restart.",
      );
    }
    const account = item.account;
    if (account) {
      if (account.admin && account.target === "new") {
        add("admin", "It's an administrator, so restoring it creates an administrator here.");
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
          `There's room for ${plural(account.profile_room, "more profile")} on this account here, so ${plural(extra, "chosen profile is", "chosen profiles are")} left out.`,
        );
      }
      for (const profile of profiles) {
        const label = `${item.label}, ${profile.name}`;
        if (profile.no_library_access) {
          add(
            `profile:${profile.id}:access`,
            "None of its allowed libraries are on this server, so it's restored with no library access.",
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
    counts.restored > 0 ? `Restored ${plural(counts.restored, "item")}` : "Restored nothing",
  ];
  if (counts.unchanged > 0) parts.push(`${counts.unchanged} already the same`);
  if (counts.left_out > 0) parts.push(`${counts.left_out} left out`);
  parts.push(counts.failed > 0 ? `${counts.failed} failed` : "nothing failed");
  return `${parts.join(", ")}.`;
}
