import type { SettingsSectionEntry } from "@/api/types";
import { homeRowsPath, type RowLinkCollection } from "@/lib/homeRows/rowLinks";
import type { PageRef, Surface } from "@/lib/homeRows/types";

import { t } from "@/i18n";

import { joinNames, libraryPageLabel, nameWithPlace } from "./copy";

/**
 * A Home or library page row that shows a collection: an administrator's row
 * (a server collection), or one on this profile's own pages (`surface:
 * "profile"`, a personal collection).
 */
export interface CollectionRow {
  id: string;
  page: PageRef;
  /** Whose rows: the administrator's (the default) or this profile's. */
  surface?: Surface;
  title: string;
  enabled: boolean;
  /** Its place on the page, counting from 1. */
  position: number;
  /** How many rows the page has, turned-off rows included. */
  pageRowCount: number;
}

/** The rows that show a collection, as read for the editor or a delete. */
export type RowsState =
  | { status: "loading" }
  | { status: "error"; onRetry: () => void }
  | { status: "ready"; rows: readonly CollectionRow[] };

type LibraryNames = ReadonlyMap<number, string>;

/** "Home" or "Kids page"; on this profile's own pages "My Home" or "My Kids page". */
export function rowPlace(page: PageRef, names: LibraryNames, surface: Surface = "admin"): string {
  if (page.kind === "home")
    return surface === "profile"
      ? t("web.collections.rows.myHome")
      : t("web.collections.rows.home");
  const library = libraryName(names, page.libraryId);
  return surface === "profile"
    ? t("web.collections.rows.myLibraryPage", { library })
    : libraryPageLabel(library);
}

/** A library's name, or a neutral word while it isn't known. */
function libraryName(names: LibraryNames, libraryId: number): string {
  return names.get(libraryId) ?? t("web.collections.rows.unknownLibrary");
}

/** `rowPlace` inside a sentence: "Home", "the Kids page", "my Home" or "my Kids page". */
export function rowPlaceInSentence(
  page: PageRef,
  names: LibraryNames,
  surface: Surface = "admin",
): string {
  if (page.kind === "home")
    return surface === "profile"
      ? t("web.collections.rows.myHomeInSentence")
      : t("web.collections.rows.home");
  const library = libraryName(names, page.libraryId);
  return surface === "profile"
    ? t("web.collections.rows.myLibraryPageInSentence", { library })
    : t("web.collections.rows.theLibraryPage", { library });
}

/**
 * "Home · row 6 of 9", with "Turned off" for an administrator's row nobody
 * sees and "Hidden" for one this profile hid.
 */
export function rowMeta(row: CollectionRow, names: LibraryNames): string {
  const where = t("web.collections.rows.rowMeta", {
    place: rowPlace(row.page, names, row.surface),
    position: row.position,
    total: row.pageRowCount,
  });
  if (row.enabled) return where;
  const state =
    row.surface === "profile"
      ? t("web.collections.rows.hidden")
      : t("web.collections.rows.turnedOff");
  return `${where} · ${state}`;
}

/** "Studio Ghibli (Home)": each row by title and place, for naming rows in a message. */
export function rowLabels(rows: readonly CollectionRow[], names: LibraryNames): string[] {
  return rows.map((row) => nameWithPlace(row.title, rowPlace(row.page, names)));
}

/**
 * "Home and the Kids page", "the Kids and Movies pages": the pages `rows` sit
 * on. This profile's own rows read "my Home and my Kids page".
 */
export function rowPlaces(rows: readonly CollectionRow[], names: LibraryNames): string | null {
  const mine = rows.some((row) => row.surface === "profile");
  const home = rows.some((row) => row.page.kind === "home");
  const pages = [
    ...new Set(
      rows.flatMap((row) =>
        row.page.kind === "library" ? [libraryName(names, row.page.libraryId)] : [],
      ),
    ),
  ];
  const parts = [
    ...(home
      ? [mine ? t("web.collections.rows.myHomeInSentence") : t("web.collections.rows.home")]
      : []),
    ...(pages.length > 0
      ? [
          t(mine ? "web.collections.rows.myLibraryPages" : "web.collections.rows.theLibraryPages", {
            count: pages.length,
            libraries: joinNames(pages),
          }),
        ]
      : []),
  ];
  return parts.length > 0 ? joinNames(parts) : null;
}

/** The header's "On Home and the Kids page": where viewers see it in a row, turned-off rows left out. */
export function onRowsLine(rows: readonly CollectionRow[], names: LibraryNames): string | null {
  const places = rowPlaces(
    rows.filter((row) => row.enabled),
    names,
  );
  return places ? t("web.collections.rows.onPlaces", { places }) : null;
}

interface NamedLibrary {
  id: number;
  name: string;
}

/**
 * The library pages a collection can be added to as a row: the libraries it's
 * in first, then the others, each in the order given.
 */
export function rowPages(
  collectionLibraries: readonly NamedLibrary[],
  allLibraries: readonly NamedLibrary[],
): { bound: NamedLibrary[]; others: NamedLibrary[] } {
  const boundIds = new Set(collectionLibraries.map((library) => library.id));
  return {
    bound: allLibraries.filter((library) => boundIds.has(library.id)),
    others: allLibraries.filter((library) => !boundIds.has(library.id)),
  };
}

/**
 * Admin Home rows on `page` with Add row open on the collection. With
 * `returnTo` (the editor), Home rows comes back there after Add row.
 */
export function addRowPath(collectionId: string, page: PageRef, returnTo?: string): string {
  return homeRowsPath("admin", page, {
    add: `collection:library:${collectionId}`,
    ...(returnTo ? { return: returnTo } : {}),
  });
}

/**
 * Settings > Home Screen on `page` with Add row open on `collection`: the
 * viewer's own Home or library page. With `returnTo` (the personal editor),
 * Home Screen comes back there after Add row.
 */
export function addToMyHomePath(
  collection: RowLinkCollection,
  page: PageRef,
  returnTo?: string,
): string {
  return homeRowsPath("profile", page, {
    add: `collection:${collection.source}:${collection.id}`,
    ...(returnTo ? { return: returnTo } : {}),
  });
}

/** Home rows (the administrator's, or this profile's Home Screen) with the row open in Edit row. */
export function openRowPath(row: Pick<CollectionRow, "id" | "page" | "surface">): string {
  return homeRowsPath(row.surface ?? "admin", row.page, { edit: row.id });
}

/**
 * The rows on this profile's own pages that show personal collection
 * `collectionId`, from each page's rows in order (Settings > Home Screen's
 * read of the page). A row's place is its rank on its page, hidden rows
 * counted.
 */
export function profileCollectionRows(
  collectionId: string,
  pages: ReadonlyArray<{ page: PageRef; sections: readonly SettingsSectionEntry[] }>,
): CollectionRow[] {
  return pages.flatMap(({ page, sections }) =>
    sections.flatMap((section, index) =>
      section.section_type === "collection" && section.config?.user_collection_id === collectionId
        ? [
            {
              id: section.id,
              page,
              surface: "profile" as const,
              title: section.title,
              enabled: !section.hidden,
              position: index + 1,
              pageRowCount: sections.length,
            },
          ]
        : [],
    ),
  );
}

/**
 * The library pages a personal collection can show on: the libraries it
 * matches, or every library the profile sees when it names none.
 */
export function matchedLibraries<Library extends { id: number }>(
  libraries: readonly Library[],
  libraryIds: readonly number[],
): Library[] {
  if (libraryIds.length === 0) return [...libraries];
  const ids = new Set(libraryIds);
  return libraries.filter((library) => ids.has(library.id));
}
