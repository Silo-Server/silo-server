import type {
  RestoreErrorCode,
  RestoreItem,
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
  collections: "Collections and artwork",
};

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

/** A selection is a set of `section:id` keys, the form `requires` uses. */
export type SelectionKeys = ReadonlySet<string>;

export function itemKey(section: RestoreSectionId, id: string): string {
  return `${section}:${id}`;
}

export interface IndexEntry {
  section: RestoreSectionId;
  item: RestoreItem;
  key: string;
}

/** Every item of a preview by its key, in restore order. */
export function indexItems(set: RestoreSet): Map<string, IndexEntry> {
  const index = new Map<string, IndexEntry>();
  for (const section of set.sections) {
    for (const item of section.items) {
      const key = itemKey(section.id, item.id);
      index.set(key, { section: section.id, item, key });
    }
  }
  return index;
}

export function entrySelectable(entry: IndexEntry | undefined): boolean {
  return entry?.item.selectable ?? false;
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
 * Chooses items and what they require (a collection's library). An item whose requirement cannot be chosen stays unchosen, and
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
    const required = entry.item.requires.map((requiredKey) => index.get(requiredKey));
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
    if (entry && entry.item.requires.some((requiredKey) => !next.has(requiredKey))) {
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

/** The chosen items, in restore order. */
export function chosenItems(set: RestoreSet, selection: SelectionKeys): IndexEntry[] {
  return [...indexItems(set).values()].filter((entry) => selection.has(entry.key));
}

/** The restore request's `selection` for the chosen keys. */
export function selectionRequest(set: RestoreSet, selection: SelectionKeys): RestoreSelection {
  const request: Required<RestoreSelection> = {
    libraries: [],
    plugins: [],
    settings: [],
    collections: [],
    home_sections: [],
  };
  for (const entry of chosenItems(set, selection)) request[entry.section].push(entry.item.id);
  return request;
}

/** Settings on a page that a restore would change. */
export function differingSettings(item: RestoreItem) {
  return (item.settings ?? []).filter((setting) => setting.status === "differs");
}

/** What restoring one item does, as a sentence for "Silo will". */
function itemSentence(entry: IndexEntry): string {
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

/**
 * Starter-pack collections count up into one or two sentences; every other
 * item gets its own.
 */
function itemSentences(entries: readonly IndexEntry[]): string[] {
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
function homeSectionSentences(entries: readonly IndexEntry[]): string[] {
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
export function restorePlan(set: RestoreSet, selection: SelectionKeys): PlanSection[] {
  const chosen = chosenItems(set, selection);
  return set.sections
    .map((section) => {
      const inSection = chosen.filter((entry) => entry.section === section.id);
      return {
        section: section.id,
        title: SECTION_TITLES[section.id],
        sentences:
          section.id === "home_sections"
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
