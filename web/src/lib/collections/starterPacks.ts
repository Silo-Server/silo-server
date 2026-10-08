import type { Library, PageSectionConfig } from "@/api/types";
import type { components } from "@/api/v2/schema";
import type {
  ApplyCollectionTemplateBundleFeaturedRequest,
  ApplyCollectionTemplateBundleResponse,
  CollectionTemplateBundleApplyEntry,
} from "@/lib/collectionTemplates";

/**
 * Starter packs are the server's template bundles: one apply creates a synced
 * list per template in every chosen library that fits it. Everything here
 * reads the bundle list's template summaries, never the admin template
 * catalog, so Discover and Franchise templates keep their titles.
 */

export type PackTemplate = components["schemas"]["CollectionTemplateSummary"];
type TemplateBundle = components["schemas"]["CollectionTemplateBundle"];

/** The bundle the server builds from every other bundle. */
const EVERYTHING_PACK_ID = "all_defaults";

/** Above this many new lists, Starter packs warns about the first syncs' load. */
export const SYNC_LOAD_THRESHOLD = 30;

export interface StarterPack {
  id: string;
  title: string;
  description: string;
  /** The pack's templates in bundle order, without those that need setup. */
  templates: PackTemplate[];
  /** The pack of every other pack's lists, listed last. */
  everything: boolean;
}

/**
 * The packs to offer. Templates that need setup (the franchise placeholder,
 * which creates an empty list that can't sync) are never shown; the server
 * still creates them when their pack is applied.
 */
export function starterPacksOf(bundles: readonly TemplateBundle[]): StarterPack[] {
  const packs = bundles
    .map((bundle) => ({
      id: bundle.id,
      title: bundle.title,
      description: bundle.description,
      templates: bundle.templates.filter((template) => !template.needs_setup),
      everything: bundle.id === EVERYTHING_PACK_ID,
    }))
    .filter((pack) => pack.templates.length > 0);
  return [...packs.filter((pack) => !pack.everything), ...packs.filter((pack) => pack.everything)];
}

/**
 * Whether a template's lists can go in a library of this type. It follows the
 * server's `templateEligibleForLibrary`, except that library types it doesn't
 * name (ebooks, podcasts) take nothing here: a movie list has no place there.
 */
export function templateFitsLibrary(mediaKind: string, libraryType: string): boolean {
  switch (libraryType.trim().toLowerCase()) {
    case "":
    case "mixed":
      return true;
    case "movie":
    case "movies":
      return mediaKind === "movie" || mediaKind === "mixed";
    case "series":
    case "tv":
    case "show":
    case "shows":
    case "tvshows":
      return mediaKind === "tv" || mediaKind === "mixed";
    case "audiobook":
    case "audiobooks":
      return mediaKind === "audiobook" || mediaKind === "mixed";
    default:
      return false;
  }
}

/** The pack's lists that fit a library, in pack order. */
export function heroTemplates(pack: StarterPack, library: Library): PackTemplate[] {
  return pack.templates.filter((template) =>
    templateFitsLibrary(template.media_kind, library.type),
  );
}

/** The enabled libraries that can take at least one of the pack's lists. */
export function packLibraries(pack: StarterPack, libraries: readonly Library[]): Library[] {
  return libraries.filter(
    (library) => library.enabled !== false && heroTemplates(pack, library).length > 0,
  );
}

/** The page's library when the pack fits it, otherwise every library it fits. */
export function defaultPackLibraryIds(
  pack: StarterPack,
  libraries: readonly Library[],
  preferredLibraryId: number | null,
): number[] {
  const ids = packLibraries(pack, libraries).map((library) => library.id);
  return preferredLibraryId !== null && ids.includes(preferredLibraryId)
    ? [preferredLibraryId]
    : ids;
}

// --- What will happen -------------------------------------------------------

export interface PackListEntry {
  templateId: string;
  title: string;
  /** The template pins its collections to the start of their shelf. */
  pinned: boolean;
  /** Why the list couldn't be added, in words (failed entries only). */
  reason?: string;
}

export interface PackLibraryRow {
  libraryId: number;
  libraryName: string;
  added: PackListEntry[];
  existing: PackListEntry[];
  failed: PackListEntry[];
  /** "2 TV lists", or "" when every list fits. */
  notForLibrary: string;
}

const EXISTING_REASONS = new Set(["already_exists", "already_exists_delete_failed"]);

// The server's reason codes for a list or hero it couldn't add, in words.
// Anything else (v2 sends `operation_failed` for raw errors) gets the fallback.
const FAILURE_REASONS: Record<string, string> = {
  ineligible_library: "that library can't take this list",
  collection_not_available: "its list wasn't added to that library",
  template_not_in_bundle: "that list isn't in this pack",
  library_not_selected: "that library isn't one you picked",
  template_not_found: "this server doesn't have that list anymore",
};

/** Why a list or hero couldn't be added, as a clause to follow a colon. */
export function failureReason(code: string | undefined): string {
  return FAILURE_REASONS[code ?? ""] ?? "something went wrong";
}

/**
 * One row per chosen library from a dry run or a finished apply: the lists it
 * gets, the ones it already has, the ones that failed, and how many lists
 * aren't for it. Entries of hidden templates are left out.
 */
export function packPlan(
  result: ApplyCollectionTemplateBundleResponse,
  pack: StarterPack,
  libraryIds: readonly number[],
): PackLibraryRow[] {
  const templates = new Map(pack.templates.map((template) => [template.id, template]));
  const shown = (entry: CollectionTemplateBundleApplyEntry) => templates.has(entry.template_id);
  const listEntry = (entry: CollectionTemplateBundleApplyEntry, reason?: string) => ({
    templateId: entry.template_id,
    title: entry.template_title,
    pinned: templates.get(entry.template_id)?.featured ?? false,
    ...(reason === undefined ? {} : { reason }),
  });
  return libraryIds.flatMap((libraryId) => {
    const own = (entries: CollectionTemplateBundleApplyEntry[] | undefined) =>
      (entries ?? []).filter((entry) => entry.library_id === libraryId && shown(entry));
    const created = own(result.created);
    const skipped = own(result.skipped);
    const failed = own(result.failed);
    const name = [...created, ...skipped, ...failed][0]?.library_name;
    if (name === undefined) return [];
    const ineligible = skipped.filter((entry) => entry.reason === "ineligible_library");
    return [
      {
        libraryId,
        libraryName: name,
        added: created.map((entry) => listEntry(entry)),
        existing: skipped
          .filter((entry) => EXISTING_REASONS.has(entry.reason ?? ""))
          .map((entry) => listEntry(entry)),
        failed: failed.map((entry) => listEntry(entry, failureReason(entry.reason))),
        notForLibrary: listCount(
          ineligible.map((entry) => templates.get(entry.template_id)?.media_kind),
        ),
      },
    ];
  });
}

/** A finished apply's rows, for every library it created or failed a list in. */
export function appliedRows(
  result: ApplyCollectionTemplateBundleResponse,
  pack: StarterPack,
): PackLibraryRow[] {
  const libraryIds = new Set(
    [...result.created, ...result.failed].map((entry) => entry.library_id),
  );
  return packPlan(result, pack, [...libraryIds]);
}

/**
 * Whether a finished apply leaves the pack added: a new list landed, or
 * nothing failed (its lists were all there already). A run where every new
 * list or hero failed is not.
 */
export function packAdded(result: ApplyCollectionTemplateBundleResponse, pack: StarterPack) {
  const rows = appliedRows(result, pack);
  if (rows.some((row) => row.added.length > 0)) return true;
  return rows.every((row) => row.failed.length === 0) && result.featured_failed.length === 0;
}

export function plural(count: number, word: string) {
  return `${count} ${word}${count === 1 ? "" : "s"}`;
}

/** A finished apply's heading, the same in the dialog and the page's job banner. */
export function packResultHeading(packTitle: string, succeeded: boolean, added: boolean) {
  if (!succeeded) return `${packTitle} wasn't added`;
  return added ? `${packTitle} added` : `${packTitle} finished with problems`;
}

/** A finished apply in a line, counting only the lists the pack shows. */
export function packResultSummary(
  result: ApplyCollectionTemplateBundleResponse,
  pack: StarterPack,
): string {
  const rows = appliedRows(result, pack);
  const added = rows.reduce((sum, row) => sum + row.added.length, 0);
  const failed = rows.reduce((sum, row) => sum + row.failed.length, 0);
  const heroes = result.featured.length;
  const heroesFailed = result.featured_failed.length;
  return [
    added > 0 ? `Added ${plural(added, "list")}.` : "Nothing new was added.",
    failed > 0 ? `${plural(failed, "list")} couldn't be added.` : "",
    heroes > 0 ? `Set ${plural(heroes, "hero banner")}.` : "",
    heroesFailed > 0 ? `${plural(heroesFailed, "hero banner")} couldn't be set.` : "",
  ]
    .filter(Boolean)
    .join(" ");
}

function listCount(kinds: readonly (string | undefined)[]): string {
  if (kinds.length === 0) return "";
  const kind = kinds.every((value) => value === kinds[0]) ? kinds[0] : undefined;
  const noun = kind === "movie" ? "movie list" : kind === "tv" ? "TV list" : "list";
  return plural(kinds.length, noun);
}

// --- Hero banners -----------------------------------------------------------

/** A hero line's choice that leaves the page's current hero alone. */
export const KEEP_CURRENT = "keep";

export interface HeroChoices {
  /** KEEP_CURRENT or `${libraryId}:${templateId}`: Home's hero needs the library its list is in. */
  home: string;
  /** Per library page: KEEP_CURRENT or a template id. */
  libraries: Record<number, string>;
}

// Today's default heroes, most wanted first.
const LIBRARY_HERO_PREFERENCE = [
  "tmdb_trending_movies_week",
  "tmdb_trending_tv_week",
  "tmdb_popular_movies",
  "tmdb_popular_tv",
];
const HOME_HERO_PREFERENCE = ["tmdb_trending_movies_week", "tmdb_trending_tv_week"];

function fits(pack: StarterPack, templateId: string, library: Library) {
  return heroTemplates(pack, library).some((template) => template.id === templateId);
}

function defaultLibraryHero(pack: StarterPack, library: Library): string {
  return (
    LIBRARY_HERO_PREFERENCE.find((id) => fits(pack, id, library)) ??
    heroTemplates(pack, library)[0]?.id ??
    KEEP_CURRENT
  );
}

function defaultHomeHero(pack: StarterPack, libraries: readonly Library[]): string {
  for (const id of HOME_HERO_PREFERENCE) {
    const library = libraries.find((candidate) => fits(pack, id, candidate));
    if (library) return `${library.id}:${id}`;
  }
  for (const library of libraries) {
    const id = defaultLibraryHero(pack, library);
    if (id !== KEEP_CURRENT) return `${library.id}:${id}`;
  }
  return KEEP_CURRENT;
}

/** Splits Home's `${libraryId}:${templateId}` choice. */
function parseHomeHero(value: string): { libraryId: number; templateId: string } | null {
  const separator = value.indexOf(":");
  const libraryId = Number(value.slice(0, separator));
  const templateId = value.slice(separator + 1);
  if (separator < 1 || !Number.isInteger(libraryId) || libraryId <= 0 || !templateId) return null;
  return { libraryId, templateId };
}

/** Whether Home's choice still works: Keep current, or a list in a chosen library that fits it. */
function homePickFits(pack: StarterPack, libraries: readonly Library[], choice: string): boolean {
  if (choice === KEEP_CURRENT) return true;
  const pick = parseHomeHero(choice);
  const library = libraries.find((candidate) => candidate.id === pick?.libraryId);
  return pick !== null && library !== undefined && fits(pack, pick.templateId, library);
}

/**
 * The heroes to show for the chosen libraries: each pick that still fits, and
 * today's default for every page without one.
 */
export function effectiveHeroes(
  pack: StarterPack,
  libraries: readonly Library[],
  picked: Partial<HeroChoices>,
): HeroChoices {
  return {
    home:
      picked.home !== undefined && homePickFits(pack, libraries, picked.home)
        ? picked.home
        : defaultHomeHero(pack, libraries),
    libraries: Object.fromEntries(
      libraries.map((library) => {
        const choice = picked.libraries?.[library.id];
        const valid =
          choice !== undefined && (choice === KEEP_CURRENT || fits(pack, choice, library));
        return [library.id, valid ? choice : defaultLibraryHero(pack, library)];
      }),
    ),
  };
}

/** The apply body's `featured` member: only the pages that get a new hero. */
export function featuredRequest(
  heroes: HeroChoices,
): ApplyCollectionTemplateBundleFeaturedRequest | undefined {
  const home = parseHomeHero(heroes.home);
  const libraries = Object.fromEntries(
    Object.entries(heroes.libraries).filter(([, templateId]) => templateId !== KEEP_CURRENT),
  );
  const request: ApplyCollectionTemplateBundleFeaturedRequest = {
    ...(home ? { home: { library_id: home.libraryId, template_id: home.templateId } } : {}),
    ...(Object.keys(libraries).length > 0 ? { libraries } : {}),
  };
  return request.home || request.libraries ? request : undefined;
}

/** The title of a page's hero today: its first shown row marked as one. */
export function currentHero(sections: readonly PageSectionConfig[]): string | null {
  return sections.find((section) => section.featured && section.enabled)?.title ?? null;
}
