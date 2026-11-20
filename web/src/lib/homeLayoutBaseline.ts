import type { components } from "@/api/v2/schema";

export type ProfileSectionBaseline = components["schemas"]["ProfileSectionBaseline"];
export type ProfileSectionBaselineRow = components["schemas"]["ProfileSectionBaselineRow"];

export type HomeLayoutBaselineMode = "baseline" | "unmatched" | "legacy";

export interface PageHideCandidatePlan {
  mode: HomeLayoutBaselineMode;
  candidates: ProfileSectionBaselineRow[];
  defaultChecked: boolean;
}

export function homeLayoutPageKey(page: { scope: "home" | "library"; libraryId?: number }): string {
  return `${page.scope}:${page.libraryId ?? ""}`;
}

/**
 * Determines which pre-override server rows on a same-server page can be
 * hidden during import and whether they start checked:
 *
 * - A valid baseline (present, non-empty, overlapping at least one active row
 *   on the page) offers only rows whose token was absent at export, checked by
 *   default.
 * - A legacy file (no baseline field) or an unmatched/truncated baseline offers
 *   all visible server rows not mentioned in `overrides`, unchecked by default
 *   so untouched defaults are never wiped silently.
 */
export function planPageHideCandidates(
  page: { overrides: readonly { section_id?: string }[]; serverSectionTokens?: readonly string[] },
  baseline: ProfileSectionBaseline,
): PageHideCandidatePlan {
  const mentionedIds = new Set<string>();
  for (const override of page.overrides) {
    if (override.section_id) mentionedIds.add(override.section_id);
  }
  const hasBaselineField = page.serverSectionTokens !== undefined;
  const baselineTokens = new Set(page.serverSectionTokens ?? []);
  const activeOverlap =
    hasBaselineField &&
    baselineTokens.size > 0 &&
    baseline.active_section_tokens.some((token) => baselineTokens.has(token));

  if (activeOverlap) {
    return {
      mode: "baseline",
      defaultChecked: true,
      candidates: baseline.visible_sections.filter(
        (row) => !mentionedIds.has(row.id) && !baselineTokens.has(row.token),
      ),
    };
  }

  return {
    mode: hasBaselineField ? "unmatched" : "legacy",
    defaultChecked: false,
    candidates: baseline.visible_sections.filter((row) => !mentionedIds.has(row.id)),
  };
}
