/**
 * Rule rows on the profile surface. The server refuses a profile save that
 * holds a row of an admin-only kind (or a filter kind) the profile added,
 * unless the account is an admin or the server lets profiles add rule rows
 * (#1118). The import of a layout file applies the same test.
 */
import type { RecipeCatalogResponse } from "@/lib/recipes";
import { FILTER_SECTION_TYPES } from "@/lib/sectionTypes";

/** The kinds the server gates: the catalog's admin-only kinds and the filter kinds. */
export function ruleRowKinds(catalog: RecipeCatalogResponse | undefined): ReadonlySet<string> {
  const kinds = new Set(FILTER_SECTION_TYPES);
  for (const defs of Object.values(catalog?.categories ?? {})) {
    for (const def of defs ?? []) if (def.admin_only) kinds.add(def.type);
  }
  return kinds;
}
