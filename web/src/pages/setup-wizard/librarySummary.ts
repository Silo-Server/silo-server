import type { Library } from "@/api/types";

/** One line for the rail and the recap: the library's name, or a count. */
export function librarySummary(libraries: Library[]): string {
  if (libraries.length === 0) return "None yet";
  if (libraries.length === 1) return libraries[0]!.name;
  return `${libraries.length} libraries`;
}
