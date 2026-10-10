import { sectionLibraryFilterIds } from "@/lib/sectionLibraryFilter";
import { isTraktConfig } from "@/lib/sectionTypes";
import type { RowDraft } from "./rowDraft";
import type { LibraryPage, PageRef } from "./types";

/** The most library ids one bulk create takes. */
export const BULK_LIBRARY_LIMIT = 100;

/**
 * Kinds whose settings belong to the page they were made on: a collection or
 * hand-picked titles from one library, rules written for one library, and
 * the kinds no longer offered for new rows.
 */
const PAGE_BOUND_TYPES: ReadonlySet<string> = new Set([
  "collection",
  "custom_filter",
  "admin_curated_list",
  "genre",
  "award_winners",
]);

function namesLibrary(config: Record<string, unknown>): boolean {
  const hasLibraryId = (key: string) => {
    const value = config[key];
    return typeof value === "number" && value > 0;
  };
  return (
    sectionLibraryFilterIds(config).length > 0 ||
    hasLibraryId("generated_library_id") ||
    hasLibraryId("library_id")
  );
}

/**
 * Whether a row can be copied to other library pages as it is. The bulk
 * create sends one config to every page, so only ready-made kinds whose
 * settings name no library qualify. Legacy Trakt rows can't be created again.
 * Hero rows are handled by the caller: a new hero row stays on its own page,
 * and copies of an existing one are never heroes.
 */
export function canCopyToLibraries(row: {
  sectionType: string;
  config: Record<string, unknown>;
}): boolean {
  return (
    !PAGE_BOUND_TYPES.has(row.sectionType) &&
    !namesLibrary(row.config) &&
    !isTraktConfig(row.config)
  );
}

/** Spellings of a library type, by the kind they name (as in internal/librarykind). */
const LIBRARY_KINDS: Record<string, string> = {
  movie: "movie",
  movies: "movie",
  series: "tv",
  tv: "tv",
  show: "tv",
  tvshows: "tv",
  audiobook: "audiobook",
  audiobooks: "audiobook",
  ebook: "ebook",
  ebooks: "ebook",
  podcast: "podcast",
  podcasts: "podcast",
};

function libraryKindOf(page: LibraryPage | undefined): string | undefined {
  const type = page?.libraryType?.trim().toLowerCase();
  return type ? (LIBRARY_KINDS[type] ?? type) : undefined;
}

/**
 * The library kind a row's titles come from: "page" for a row set to a media
 * scope (a default "Recently Added Movies" row), which fits its own page's
 * kind; the book kind for Continue Listening, Continue Reading, and Next in
 * Series, which follows audiobook series; undefined for a row that fits any
 * library.
 */
function rowLibraryKind(row: {
  sectionType: string;
  config: Record<string, unknown>;
}): "page" | "audiobook" | "ebook" | undefined {
  const { config } = row;
  if (typeof config.media_scope === "string" && config.media_scope !== "") return "page";
  if (row.sectionType === "next_in_series") return "audiobook";
  if (row.sectionType !== "continue_watching") return undefined;
  const lower = (value: unknown) => (typeof value === "string" ? value.trim().toLowerCase() : "");
  const continueType = lower(config.continue_type);
  if (continueType === "listening") return "audiobook";
  if (continueType === "reading") return "ebook";
  if (continueType !== "") return undefined;
  const filterType = lower(config.filter_type);
  return filterType === "audiobook" || filterType === "ebook" ? filterType : undefined;
}

/**
 * The library pages a row on page `currentId` can go to, that page included.
 * A row that shows one kind of title would be empty or wrong in another kind
 * of library, so it only goes to pages of that kind. The kind comes from the
 * row where it says one, so a book row left on a movie page still reaches
 * the book libraries.
 */
export function copyTargetPages(
  row: { sectionType: string; config: Record<string, unknown> },
  pages: readonly LibraryPage[],
  currentId: number,
): LibraryPage[] {
  const rowKind = rowLibraryKind(row);
  if (rowKind === undefined) return [...pages];
  const kind =
    rowKind === "page" ? libraryKindOf(pages.find((page) => page.id === currentId)) : rowKind;
  return pages.filter(
    (page) => page.id === currentId || (kind !== undefined && libraryKindOf(page) === kind),
  );
}

/**
 * The other library pages an Add row draft also goes to: none on Home, for a
 * hero row, or for a kind that can't be copied, and only pages the row fits.
 * Never the current page.
 */
export function libraryCopyIds(
  draft: Pick<RowDraft, "sectionType" | "config" | "hero" | "extraLibraryIds">,
  page: PageRef,
  pages: readonly LibraryPage[],
): number[] {
  if (page.kind !== "library" || draft.hero || !canCopyToLibraries(draft)) return [];
  const fits = new Set(copyTargetPages(draft, pages, page.libraryId).map((p) => p.id));
  return [...new Set(draft.extraLibraryIds ?? [])].filter(
    (id) => id !== page.libraryId && fits.has(id),
  );
}
