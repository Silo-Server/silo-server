import { V2ProblemError } from "@/api/v2/request";
import type { components } from "@/api/v2/schema";
import { FILTER_SECTION_TYPES, isTraktConfig } from "@/lib/sectionTypes";

// Home layout export and import: a profile's saved section overrides for the
// home page and each library page, written to a JSON file that another
// profile, on this server or another one, can load.
//
// An override can point at things only one server knows: admin section IDs
// (minted randomly per server), library IDs, collection IDs and profile IDs.
// The file records the exporting server's identity and library names. On the
// same server every reference still resolves, so an import skips only what the
// importing profile can't use; on another server it maps libraries by name and
// type and skips what cannot carry over.

type SectionOverrideRead = components["schemas"]["SectionOverride"];
export type SectionOverrideWrite = components["schemas"]["SectionOverrideWrite"];

export const HOME_LAYOUT_FORMAT = "silo-home-layout";
export const HOME_LAYOUT_VERSION = 1;
/**
 * The largest export, in bytes. Export refuses to write a larger file and
 * import refuses to read one, so every export can be imported.
 */
export const HOME_LAYOUT_MAX_LENGTH = 5 * 1024 * 1024;

export type HomeLayoutScope = "home" | "library";

export interface HomeLayoutLibrary {
  id: number;
  name: string;
  type: string;
}

export interface HomeLayoutPage {
  scope: HomeLayoutScope;
  /** Set on a library page: the exporting server's library ID. */
  library_id?: number;
  overrides: SectionOverrideWrite[];
}

export interface HomeLayoutFile {
  format: typeof HOME_LAYOUT_FORMAT;
  version: typeof HOME_LAYOUT_VERSION;
  exported_at: string;
  server_id: string;
  /** The exporting profile's libraries, so another server can map them by name. */
  libraries: HomeLayoutLibrary[];
  hide_watched_items?: boolean;
  pages: HomeLayoutPage[];
}

export interface HomeLayoutSourcePage {
  scope: HomeLayoutScope;
  libraryId?: number;
  overrides: SectionOverrideRead[];
}

/** The write shape of a stored override, without its ID and timestamps. */
export function overrideToWrite(override: SectionOverrideRead): SectionOverrideWrite {
  const write: SectionOverrideWrite = {};
  if (override.section_id) write.section_id = override.section_id;
  if (override.position != null) write.position = override.position;
  if (override.hidden) write.hidden = true;
  if (override.removed) write.removed = true;
  if (override.section_type) write.section_type = override.section_type;
  if (override.title) write.title = override.title;
  if (override.featured != null) write.featured = override.featured;
  if (override.item_limit != null) write.item_limit = override.item_limit;
  if (override.config !== undefined) write.config = override.config;
  if (override.is_user_added) write.is_user_added = true;
  if (override.user_section_type) write.user_section_type = override.user_section_type;
  if (override.user_config !== undefined) write.user_config = override.user_config;
  if (override.user_title) write.user_title = override.user_title;
  return write;
}

/** Builds the export file. Pages without saved overrides are left out. */
export function buildHomeLayoutFile(input: {
  serverId: string;
  exportedAt: Date;
  libraries: HomeLayoutLibrary[];
  hideWatchedItems?: boolean;
  pages: HomeLayoutSourcePage[];
}): HomeLayoutFile {
  return {
    format: HOME_LAYOUT_FORMAT,
    version: HOME_LAYOUT_VERSION,
    exported_at: input.exportedAt.toISOString(),
    server_id: input.serverId,
    libraries: input.libraries.map(({ id, name, type }) => ({ id, name, type })),
    ...(input.hideWatchedItems !== undefined ? { hide_watched_items: input.hideWatchedItems } : {}),
    pages: input.pages
      .filter((page) => page.overrides.length > 0)
      .map((page) => ({
        scope: page.scope,
        ...(page.scope === "library" ? { library_id: page.libraryId } : {}),
        overrides: page.overrides.map(overrideToWrite),
      })),
  };
}

export type HomeLayoutParseResult =
  | { ok: true; file: HomeLayoutFile }
  | { ok: false; error: string };

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value > 0;
}

function boundedString(value: unknown, max: number): value is string {
  return typeof value === "string" && value.length <= max;
}

// Keeps the members the write contract accepts, each only when its type
// matches, so a hand-edited file cannot smuggle other keys into the request.
function sanitizeOverride(raw: unknown): SectionOverrideWrite | null {
  if (!isRecord(raw)) return null;
  const write: SectionOverrideWrite = {};
  if (boundedString(raw.section_id, 128) && raw.section_id) write.section_id = raw.section_id;
  if (typeof raw.position === "number" && Number.isInteger(raw.position) && raw.position >= 0) {
    write.position = raw.position;
  }
  if (raw.hidden === true) write.hidden = true;
  if (raw.removed === true) write.removed = true;
  if (boundedString(raw.section_type, 64) && raw.section_type) {
    write.section_type = raw.section_type;
  }
  if (boundedString(raw.title, 200) && raw.title) write.title = raw.title;
  if (typeof raw.featured === "boolean") write.featured = raw.featured;
  if (isPositiveInteger(raw.item_limit)) write.item_limit = raw.item_limit;
  if (isRecord(raw.config)) write.config = raw.config;
  if (raw.is_user_added === true) write.is_user_added = true;
  if (boundedString(raw.user_section_type, 64) && raw.user_section_type) {
    write.user_section_type = raw.user_section_type;
  }
  if (isRecord(raw.user_config)) write.user_config = raw.user_config;
  if (boundedString(raw.user_title, 200) && raw.user_title) write.user_title = raw.user_title;
  return write;
}

/** Parses and validates the text of an export file. */
export function parseHomeLayoutFile(text: string): HomeLayoutParseResult {
  if (text.length > HOME_LAYOUT_MAX_LENGTH) {
    return { ok: false, error: "This file is too large to be a home layout export." };
  }
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch {
    return { ok: false, error: "This isn't valid JSON." };
  }
  if (!isRecord(data) || data.format !== HOME_LAYOUT_FORMAT) {
    return { ok: false, error: "This isn't a Silo home layout export." };
  }
  if (typeof data.version === "number" && data.version > HOME_LAYOUT_VERSION) {
    return { ok: false, error: "This export comes from a newer version of Silo." };
  }
  if (
    data.version !== HOME_LAYOUT_VERSION ||
    typeof data.server_id !== "string" ||
    !Array.isArray(data.pages)
  ) {
    return { ok: false, error: "This home layout export is incomplete or damaged." };
  }

  const libraries: HomeLayoutLibrary[] = [];
  for (const raw of Array.isArray(data.libraries) ? data.libraries : []) {
    if (
      isRecord(raw) &&
      isPositiveInteger(raw.id) &&
      typeof raw.name === "string" &&
      typeof raw.type === "string"
    ) {
      libraries.push({ id: raw.id, name: raw.name, type: raw.type });
    }
  }

  const pages: HomeLayoutPage[] = [];
  const seenPages = new Set<string>();
  for (const raw of data.pages) {
    if (!isRecord(raw) || !Array.isArray(raw.overrides)) continue;
    let page: HomeLayoutPage;
    if (raw.scope === "home") {
      page = { scope: "home", overrides: [] };
    } else if (raw.scope === "library" && isPositiveInteger(raw.library_id)) {
      page = { scope: "library", library_id: raw.library_id, overrides: [] };
    } else {
      continue;
    }
    const key = `${page.scope}:${page.library_id ?? ""}`;
    if (seenPages.has(key)) continue;
    seenPages.add(key);
    for (const override of raw.overrides) {
      const write = sanitizeOverride(override);
      if (write) page.overrides.push(write);
    }
    pages.push(page);
  }

  return {
    ok: true,
    file: {
      format: HOME_LAYOUT_FORMAT,
      version: HOME_LAYOUT_VERSION,
      exported_at: typeof data.exported_at === "string" ? data.exported_at : "",
      server_id: data.server_id,
      libraries,
      ...(typeof data.hide_watched_items === "boolean"
        ? { hide_watched_items: data.hide_watched_items }
        : {}),
      pages,
    },
  };
}

export interface HomeLayoutReferences {
  personalCollections: boolean;
  profiles: boolean;
}

/** Which kinds of account-scoped references the file's sections make. */
export function fileReferences(file: HomeLayoutFile): HomeLayoutReferences {
  const refs = { personalCollections: false, profiles: false };
  for (const page of file.pages) {
    for (const override of page.overrides) {
      for (const config of [override.config, override.user_config]) {
        if (!config) continue;
        if (nonEmptyString(config.user_collection_id)) refs.personalCollections = true;
        if (nonEmptyString(config.profile_id)) refs.profiles = true;
      }
    }
  }
  return refs;
}

export type HomeLayoutSkipReason =
  | "unknown_recipe"
  | "custom_disabled"
  | "trakt"
  | "library"
  | "collection"
  | "profile";

export const HOME_LAYOUT_SKIP_REASON_LABELS: Record<HomeLayoutSkipReason, string> = {
  unknown_recipe: "This server doesn't offer this section type",
  custom_disabled: "Only an admin can add this kind of row",
  trakt: "Trakt-backed sections can't be added again",
  library: "Uses a library this profile doesn't have here",
  collection: "Uses a collection this profile can't see here",
  profile: "Follows a profile outside this account",
};

export interface HomeLayoutImportTarget {
  serverId: string;
  libraries: HomeLayoutLibrary[];
  /** Recipe types the server's gallery lists, with whether each is admin-only. */
  recipes: ReadonlyMap<string, { adminOnly: boolean }>;
  /** The importing account is an admin, so admin-only kinds (Editor's picks) may be added. */
  allowAdminOnlyRecipes: boolean;
  /** Personal collections the importing profile can see. */
  personalCollectionIds: ReadonlySet<string>;
  /** The importing account's profiles. */
  profileIds: ReadonlySet<string>;
}

export interface HomeLayoutPlannedPage {
  scope: HomeLayoutScope;
  /** The importing server's library ID, on a library page. */
  libraryId?: number;
  label: string;
  overrides: SectionOverrideWrite[];
}

export interface HomeLayoutSkippedSection {
  page: string;
  title: string;
  reason: HomeLayoutSkipReason;
}

export interface HomeLayoutImportPlan {
  sameServer: boolean;
  pages: HomeLayoutPlannedPage[];
  /** Library pages with no matching library here, by their exported name. */
  skippedPages: string[];
  skippedSections: HomeLayoutSkippedSection[];
  /** Changes to the exporting server's own sections, which another server doesn't have. */
  skippedServerSectionChanges: number;
  hideWatchedItems?: boolean;
}

// Config members holding library IDs, read the way the backend reads them.
const LIBRARY_ID_KEYS = ["filter_library_id", "generated_library_id", "library_id"] as const;
const LIBRARY_ID_LIST_KEYS = ["filter_library", "filter_library_ids", "library_ids"] as const;

function normalizeLibraryName(name: string): string {
  return name.trim().toLocaleLowerCase();
}

// Maps each exported library ID to the importing server's library. The same
// server keeps the ID when the importing profile can open that library;
// another server needs a one-to-one match on name and type.
function buildLibraryMap(
  file: HomeLayoutFile,
  target: HomeLayoutImportTarget,
  sameServer: boolean,
): Map<number, HomeLayoutLibrary> {
  const map = new Map<number, HomeLayoutLibrary>();
  if (sameServer) {
    for (const library of target.libraries) map.set(library.id, library);
    return map;
  }
  const key = (library: HomeLayoutLibrary) =>
    `${library.type}\u0000${normalizeLibraryName(library.name)}`;
  const count = (libraries: HomeLayoutLibrary[]) => {
    const counts = new Map<string, number>();
    for (const library of libraries) counts.set(key(library), (counts.get(key(library)) ?? 0) + 1);
    return counts;
  };
  const sourceCounts = count(file.libraries);
  const targetCounts = count(target.libraries);
  for (const source of file.libraries) {
    const match = target.libraries.find((library) => key(library) === key(source));
    if (match && sourceCounts.get(key(source)) === 1 && targetCounts.get(key(match)) === 1) {
      map.set(source.id, match);
    }
  }
  return map;
}

// Rewrites the library IDs in a config through the map, or returns null when
// one of them has no library here.
function remapConfigLibraries(
  config: Record<string, unknown>,
  libraries: Map<number, HomeLayoutLibrary>,
): Record<string, unknown> | null {
  const next = { ...config };
  for (const key of LIBRARY_ID_KEYS) {
    const value = next[key];
    if (!isPositiveInteger(value)) continue;
    const mapped = libraries.get(value);
    if (!mapped) return null;
    next[key] = mapped.id;
  }
  for (const key of LIBRARY_ID_LIST_KEYS) {
    const value = next[key];
    if (!Array.isArray(value)) continue;
    const ids: unknown[] = [];
    for (const id of value) {
      if (!isPositiveInteger(id)) {
        ids.push(id);
        continue;
      }
      const mapped = libraries.get(id);
      if (!mapped) return null;
      ids.push(mapped.id);
    }
    next[key] = ids;
  }
  return next;
}

// The config the server validates and resolves for a profile-built section.
function effectiveConfig(override: SectionOverrideWrite): Record<string, unknown> {
  return override.user_config ?? override.config ?? {};
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "";
}

function sectionSkipReason(
  override: SectionOverrideWrite,
  target: HomeLayoutImportTarget,
  sameServer: boolean,
  libraries: Map<number, HomeLayoutLibrary>,
): HomeLayoutSkipReason | null {
  const recipeType = override.user_section_type || override.section_type || "";
  const isFilterType = FILTER_SECTION_TYPES.has(recipeType);
  const recipe = target.recipes.get(recipeType);
  // The gallery omits hidden recipes the server still resolves (award_winners),
  // so on the same server a saved row's type is trusted.
  if (!recipe && !isFilterType && !sameServer) return "unknown_recipe";
  if (recipe?.adminOnly && !target.allowAdminOnlyRecipes) return "custom_disabled";

  const config = effectiveConfig(override);
  // The server refuses any new override whose config names Trakt as its source.
  if (isTraktConfig(config)) return "trakt";
  if (sameServer) return sameServerReferenceProblem(config, target, libraries);
  if (nonEmptyString(config.library_collection_id) || nonEmptyString(config.user_collection_id)) {
    return "collection";
  }
  if (nonEmptyString(config.profile_id)) return "profile";
  return null;
}

/**
 * What in a section config the importing profile can't use on the same
 * server: a library filter with no library it can open, a personal
 * collection it can't see, or a pinned activity profile outside its account.
 * The feed reads a pinned profile's history by profile ID alone, so a profile
 * from another account would expose that account's viewing. Library
 * collections are left alone: sections resolve them by ID, including ones the
 * Collections tab hides, so the client has no list to check them against.
 */
function sameServerReferenceProblem(
  config: Record<string, unknown>,
  target: HomeLayoutImportTarget,
  libraries: Map<number, HomeLayoutLibrary>,
): "library" | "collection" | "profile" | null {
  if (!libraryFiltersReachable(config, libraries)) return "library";
  if (
    nonEmptyString(config.user_collection_id) &&
    !target.personalCollectionIds.has(config.user_collection_id)
  ) {
    return "collection";
  }
  if (nonEmptyString(config.profile_id) && !target.profileIds.has(config.profile_id)) {
    return "profile";
  }
  return null;
}

function positiveIds(value: unknown): number[] {
  if (isPositiveInteger(value)) return [value];
  return Array.isArray(value) ? value.filter(isPositiveInteger) : [];
}

// Whether each library filter in a config names at least one library the
// profile can open, read the way the server reads it: filter_library_ids
// together with filter_library_id, else library_ids, plus a custom filter's
// filter_library and an editorial section's library_id. An empty filter
// means every library. The server limits a section to the libraries the
// profile can open, so one reachable library is enough.
function libraryFiltersReachable(
  config: Record<string, unknown>,
  libraries: Map<number, HomeLayoutLibrary>,
): boolean {
  const flat = [
    ...positiveIds(config.filter_library_ids),
    ...positiveIds(config.filter_library_id),
  ];
  const filters = [
    flat.length > 0 ? flat : positiveIds(config.library_ids),
    positiveIds(config.filter_library),
    positiveIds(config.library_id),
  ];
  return filters.every((ids) => ids.length === 0 || ids.some((id) => libraries.has(id)));
}

function sectionTitle(override: SectionOverrideWrite): string {
  return (
    override.user_title ||
    override.title ||
    override.user_section_type ||
    override.section_type ||
    "Untitled section"
  );
}

/**
 * Works out what an import applies on this server: which pages it replaces,
 * with which overrides, and what it skips and why. `newId` names each
 * profile-built section, as the editor does when it adds one.
 */
export function planHomeLayoutImport(
  file: HomeLayoutFile,
  target: HomeLayoutImportTarget,
  newId: () => string,
): HomeLayoutImportPlan {
  const sameServer = file.server_id !== "" && file.server_id === target.serverId;
  const libraries = buildLibraryMap(file, target, sameServer);
  const exportedLibraryNames = new Map(file.libraries.map((library) => [library.id, library.name]));

  const plan: HomeLayoutImportPlan = {
    sameServer,
    pages: [],
    skippedPages: [],
    skippedSections: [],
    skippedServerSectionChanges: 0,
    ...(file.hide_watched_items !== undefined ? { hideWatchedItems: file.hide_watched_items } : {}),
  };

  for (const page of file.pages) {
    let planned: HomeLayoutPlannedPage;
    let sourceLabel = "Home";
    if (page.scope === "library") {
      sourceLabel = exportedLibraryNames.get(page.library_id!) ?? `Library ${page.library_id}`;
      const library = libraries.get(page.library_id!);
      if (!library) {
        plan.skippedPages.push(sourceLabel);
        continue;
      }
      planned = { scope: "library", libraryId: library.id, label: library.name, overrides: [] };
    } else {
      planned = { scope: "home", label: "Home", overrides: [] };
    }

    for (const override of page.overrides) {
      if (override.section_id) {
        if (!sameServer) {
          plan.skippedServerSectionChanges += 1;
          continue;
        }
        const write: SectionOverrideWrite = { ...override };
        // The editor saves an admin section's config with the profile's other
        // changes. One the importing profile can't use is dropped, so the
        // section falls back to the admin's own config.
        if (write.config && sameServerReferenceProblem(write.config, target, libraries)) {
          delete write.config;
        }
        planned.overrides.push(write);
        continue;
      }
      // A removed profile-built section never renders; nothing to carry.
      if (override.removed) continue;

      const write: SectionOverrideWrite = { ...override };
      const reason =
        sectionSkipReason(override, target, sameServer, libraries) ??
        (sameServer || remapProfileBuiltLibraries(write, libraries) ? null : "library");
      if (reason) {
        plan.skippedSections.push({ page: sourceLabel, title: sectionTitle(override), reason });
        continue;
      }
      write.id = newId();
      planned.overrides.push(write);
    }

    if (planned.overrides.length > 0) plan.pages.push(planned);
  }

  return plan;
}

// Rewrites a profile-built section's library IDs for another server, in the
// config the server uses (user_config when present, else config); false when
// one has no match. The unused config is rewritten too, or dropped when it
// can't be, since the server never reads it.
function remapProfileBuiltLibraries(
  write: SectionOverrideWrite,
  libraries: Map<number, HomeLayoutLibrary>,
): boolean {
  const used = write.user_config !== undefined ? "user_config" : "config";
  const config = write[used];
  if (!config) return true;
  const remapped = remapConfigLibraries(config, libraries);
  if (!remapped) return false;
  write[used] = remapped;
  const unused = used === "user_config" ? "config" : "user_config";
  const other = write[unused];
  if (other) {
    const otherRemapped = remapConfigLibraries(other, libraries);
    if (otherRemapped) write[unused] = otherRemapped;
    else delete write[unused];
  }
  return true;
}

function keepExisting(override: SectionOverrideRead): SectionOverrideWrite {
  return { ...overrideToWrite(override), ...(override.id ? { id: override.id } : {}) };
}

/**
 * The override set to save for one imported page, given the importing
 * profile's saved overrides for it.
 *
 * On another server the file says nothing about this server's own sections,
 * so the profile's saved changes to them stay and only its profile-built
 * sections are replaced.
 *
 * On the same server the file describes the whole page and replaces what the
 * profile saved. A change to an admin section keeps the ID of the profile's
 * saved override for that section, or gets a new one, as the server's
 * section source policy expects of every override. For a section in
 * `keepSavedSectionIds`, such as a legacy Trakt admin section, which can't
 * be changed or shown again, the profile's saved override stays as it is;
 * without one, only a hide or remove carries over.
 *
 * Either way, a saved profile-built Trakt section stays, since it can't be
 * created again.
 */
export function mergeImportedPage(
  page: HomeLayoutPlannedPage,
  existing: SectionOverrideRead[],
  sameServer: boolean,
  keepSavedSectionIds: ReadonlySet<string>,
  newId: () => string,
): SectionOverrideWrite[] {
  // A saved profile-built Trakt section can't be created again, so the
  // profile keeps it instead of losing it to the replacement.
  const savedTraktBuilt = existing
    .filter(
      (override) => !override.section_id && isTraktConfig(override.user_config ?? override.config),
    )
    .map(keepExisting);
  if (!sameServer) {
    return [
      ...existing.filter((override) => override.section_id).map(keepExisting),
      ...savedTraktBuilt,
      ...page.overrides,
    ];
  }

  // The server resolves the last saved override for a section.
  const existingBySection = new Map<string, SectionOverrideRead>();
  for (const override of existing) {
    if (override.section_id) existingBySection.set(override.section_id, override);
  }
  const imported = new Set<string>();
  const merged: SectionOverrideWrite[] = [];
  for (const override of page.overrides) {
    const sectionId = override.section_id;
    if (!sectionId) {
      merged.push(override);
      continue;
    }
    // One override per section; a second would repeat the saved ID.
    if (imported.has(sectionId)) continue;
    imported.add(sectionId);
    const saved = existingBySection.get(sectionId);
    if (keepSavedSectionIds.has(sectionId)) {
      if (saved) merged.push(keepExisting(saved));
      else if (override.hidden || override.removed) merged.push({ ...override, id: newId() });
      continue;
    }
    merged.push({ ...override, id: saved?.id || newId() });
  }
  // Dropping a saved hide of such a section would show it again.
  for (const [sectionId, saved] of existingBySection) {
    if (!imported.has(sectionId) && keepSavedSectionIds.has(sectionId)) {
      merged.push(keepExisting(saved));
    }
  }
  return [...merged, ...savedTraktBuilt];
}

/**
 * The legacy Trakt admin sections on a page, from the importing profile's
 * view of it and its saved overrides.
 */
export function legacyTraktSectionIds(
  settings: { id: string; is_custom: boolean; config?: Record<string, unknown> }[],
  existing: SectionOverrideRead[],
): Set<string> {
  const ids = new Set<string>();
  for (const section of settings) {
    if (!section.is_custom && isTraktConfig(section.config)) ids.add(section.id);
  }
  for (const override of existing) {
    if (override.section_id && isTraktConfig(override.config)) ids.add(override.section_id);
  }
  return ids;
}

/** The most overrides one page's save accepts (SectionOverrideSet in the v2 contract). */
export const HOME_LAYOUT_MAX_PAGE_OVERRIDES = 500;

type PageViewSection = { id: string; is_custom: boolean; config?: Record<string, unknown> };

/** The requests that import one page, bound to the importing profile. */
export interface HomeLayoutPageApi {
  listSaved(): Promise<SectionOverrideRead[]>;
  listView(): Promise<PageViewSection[]>;
  save(overrides: SectionOverrideWrite[]): Promise<unknown>;
}

/**
 * Saves one planned page over the profile's saved overrides. The server
 * refuses to change or drop a saved override that holds back a legacy Trakt
 * admin section, and the client can't always tell which those are: a removed
 * section isn't in the page view, and an older override can mask its source.
 * A refused save writes nothing, so a refused same-server page is retried
 * once with every saved admin-section override kept as it is; the file's
 * changes then apply only to sections the profile hadn't changed.
 * `keptSavedChanges` reports that retry.
 */
export async function importPage(
  page: HomeLayoutPlannedPage,
  sameServer: boolean,
  api: HomeLayoutPageApi,
  newId: () => string,
): Promise<{ keptSavedChanges: boolean }> {
  const [existing, view] = await Promise.all([
    api.listSaved(),
    sameServer ? api.listView() : Promise.resolve([]),
  ]);
  const legacyTrakt = legacyTraktSectionIds(view, existing);
  const save = (keep: ReadonlySet<string>) => {
    const overrides = mergeImportedPage(page, existing, sameServer, keep, newId);
    if (overrides.length > HOME_LAYOUT_MAX_PAGE_OVERRIDES) {
      throw new Error(
        `this page would have more than ${HOME_LAYOUT_MAX_PAGE_OVERRIDES} saved section changes`,
      );
    }
    return api.save(overrides);
  };
  try {
    await save(legacyTrakt);
    return { keptSavedChanges: false };
  } catch (error) {
    const saved = existing.flatMap((override) =>
      override.section_id ? [override.section_id] : [],
    );
    if (
      !sameServer ||
      !(error instanceof V2ProblemError && error.status === 422) ||
      saved.length === 0
    ) {
      throw error;
    }
    await save(new Set([...legacyTrakt, ...saved]));
    return { keptSavedChanges: true };
  }
}
