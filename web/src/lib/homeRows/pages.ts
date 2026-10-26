import type { HomeRowsPageOption, LibraryPage, PageRef } from "./types";

export const HOME_PAGE: PageRef = { kind: "home" };

/** The `?page=` value for a page: "home" or the library id. */
export function pageParam(ref: PageRef): string {
  return ref.kind === "home" ? "home" : String(ref.libraryId);
}

/**
 * Reads `?page=`. Anything that is not "home" or the id of one of the given
 * libraries opens Home, so a stale or hand-edited link still lands somewhere.
 */
export function parsePageParam(value: string | null, libraryIds: readonly number[]): PageRef {
  if (value === null || value === "home" || !/^\d+$/.test(value)) return HOME_PAGE;
  const libraryId = Number(value);
  return libraryIds.includes(libraryId) ? { kind: "library", libraryId } : HOME_PAGE;
}

export function samePage(a: PageRef, b: PageRef): boolean {
  return pageParam(a) === pageParam(b);
}

/** The page's name in sentences: "Home" or the library name. */
export function pageLabel(ref: PageRef, pages: readonly HomeRowsPageOption[]): string {
  return pages.find((page) => samePage(page.ref, ref))?.label ?? "Home";
}

/** The library pages among `pages`, by library id, in the switcher's order. */
export function libraryPagesOf(pages: readonly HomeRowsPageOption[]): LibraryPage[] {
  return pages.flatMap((page) =>
    page.ref.kind === "library"
      ? [{ id: page.ref.libraryId, label: page.label, libraryType: page.libraryType }]
      : [],
  );
}
