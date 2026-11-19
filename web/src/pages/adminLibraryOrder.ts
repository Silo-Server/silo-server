import type { Library } from "@/api/types";

export interface LibraryReorderEntry {
  id: number;
  position: number;
}

export function buildLibraryReorderEntries(libraries: Library[]): LibraryReorderEntry[] {
  return libraries.map((lib, index) => ({ id: lib.id, position: index }));
}

/**
 * The order after moving one library a single place up (-1) or down (1), or
 * null when the library is missing or already at that end.
 */
export function moveLibraryByOne<T extends { id: number }>(
  libraries: readonly T[],
  id: number,
  direction: -1 | 1,
): T[] | null {
  const from = libraries.findIndex((library) => library.id === id);
  const to = from + direction;
  if (from === -1 || to < 0 || to >= libraries.length) return null;
  const next = [...libraries];
  [next[from], next[to]] = [next[to]!, next[from]!];
  return next;
}
