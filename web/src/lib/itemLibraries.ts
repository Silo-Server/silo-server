import type { ItemFile } from "@/api/types";

/** The distinct libraries the given files live in, in ascending ID order. */
export function libraryIdsOfFiles(files: readonly Pick<ItemFile, "library_id">[]): number[] {
  const ids = new Set<number>();
  for (const file of files) {
    const id = Number(file.library_id);
    if (Number.isInteger(id) && id > 0) ids.add(id);
  }
  return [...ids].sort((a, b) => a - b);
}
