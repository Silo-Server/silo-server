import type { components } from "@/api/v2/schema";

export type ProfileSectionBaseline = components["schemas"]["ProfileSectionBaseline"];
export type ProfileSectionBaselineRow = components["schemas"]["ProfileSectionBaselineRow"];

export function homeLayoutPageKey(page: { scope: "home" | "library"; libraryId?: number }): string {
  return `${page.scope}:${page.libraryId ?? ""}`;
}

/**
 * The server rows on a same-server page that were added since the export, so
 * the import can offer to hide them.
 *
 * Only an export whose tokens still match at least one row the profile sees
 * now can tell new rows from old ones. A file without tokens, or whose tokens
 * match nothing (the server's key changed, or every row was replaced), offers
 * none and imports as it did before tokens existed.
 */
export function rowsAddedSinceExport(
  page: { overrides: readonly { section_id?: string }[]; serverSectionTokens?: readonly string[] },
  baseline: ProfileSectionBaseline,
): ProfileSectionBaselineRow[] {
  const exported = new Set(page.serverSectionTokens ?? []);
  if (!baseline.visible_sections.some((row) => exported.has(row.token))) return [];
  const mentioned = new Set(page.overrides.map((override) => override.section_id).filter(Boolean));
  return baseline.visible_sections.filter(
    (row) => !exported.has(row.token) && !mentioned.has(row.id),
  );
}
