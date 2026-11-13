import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type { ResolvedSection, ResolvedSectionLayout, SectionItem } from "@/api/types";
import { fetchRecipeCatalog } from "@/lib/recipes";

const NO_TYPES: ReadonlySet<string> = new Set();
const NO_SHOWN_COUNTS: ReadonlyMap<string, number> = new Map();

/**
 * dropCrossSectionDuplicates applies the recipes' avoid_duplicates rule to
 * sections that load one request at a time, as the server does for the
 * aggregate sections endpoint. Sections are visited in layout order; each
 * marks its items seen, and a section whose type avoids duplicates first
 * drops the items an earlier section shows. Rows arrive out of order, so
 * callers rerun this over every loaded section whenever one arrives: the
 * result then depends only on what has loaded, not on arrival order.
 * Unchanged sections keep their identity.
 *
 * `shownCounts` names the rows that loaded more cards after their first read,
 * in the order they did, each with how many cards it showed before. Those
 * later cards count as coming after every row's first cards, in that order,
 * so loading more of one row never takes a card another row already shows.
 */
export function dropCrossSectionDuplicates(
  layout: readonly ResolvedSectionLayout[],
  loadedSections: ReadonlyMap<string, ResolvedSection>,
  avoidDuplicateTypes: ReadonlySet<string>,
  shownCounts: ReadonlyMap<string, number> = NO_SHOWN_COUNTS,
): Map<string, ResolvedSection> {
  const result = new Map(loadedSections);
  if (avoidDuplicateTypes.size === 0) return result;

  const seen = new Set<string>();
  const keep = (section: ResolvedSection, items: SectionItem[]) => {
    const kept = avoidDuplicateTypes.has(section.section_type)
      ? items.filter((item) => !item.content_id || !seen.has(item.content_id))
      : items;
    for (const item of kept) {
      if (item.content_id) seen.add(item.content_id);
    }
    return kept;
  };
  const firstCards = new Map<string, { section: ResolvedSection; first: SectionItem[] }>();
  for (const entry of layout) {
    const section = loadedSections.get(entry.id);
    if (!section) continue;
    const first = keep(section, section.items.slice(0, shownCounts.get(entry.id)));
    firstCards.set(entry.id, { section, first });
  }
  const laterCards = new Map<string, SectionItem[]>();
  for (const [id, count] of shownCounts) {
    const row = firstCards.get(id);
    if (row) laterCards.set(id, keep(row.section, row.section.items.slice(count)));
  }
  for (const [id, { section, first }] of firstCards) {
    const later = laterCards.get(id) ?? [];
    const dropped = section.items.length - first.length - later.length;
    if (dropped > 0) {
      result.set(id, {
        ...section,
        items: [...first, ...later],
        total_count: Math.max(first.length + later.length, section.total_count - dropped),
      });
    }
  }
  return result;
}

/**
 * useDedupedSections is dropCrossSectionDuplicates over the loaded sections,
 * rerun whenever the layout, a loaded section, or the recipe gallery changes.
 */
export function useDedupedSections(
  layout: readonly ResolvedSectionLayout[],
  loadedSections: ReadonlyMap<string, ResolvedSection>,
  shownCounts?: ReadonlyMap<string, number>,
): Map<string, ResolvedSection> {
  const avoidDuplicateTypes = useAvoidDuplicateSectionTypes();
  return useMemo(
    () => dropCrossSectionDuplicates(layout, loadedSections, avoidDuplicateTypes, shownCounts),
    [avoidDuplicateTypes, layout, loadedSections, shownCounts],
  );
}

/**
 * useAvoidDuplicateSectionTypes returns the section types whose recipe sets
 * avoid_duplicates, from the recipe gallery (shared with the settings pages'
 * cache entry). Until it loads, or if it fails, no section type avoids
 * duplicates and rows show as served.
 */
function useAvoidDuplicateSectionTypes(): ReadonlySet<string> {
  const { data } = useQuery({
    queryKey: ["recipe-catalog"],
    queryFn: fetchRecipeCatalog,
    staleTime: 5 * 60 * 1000,
  });
  return useMemo(() => {
    if (!data) return NO_TYPES;
    const types = new Set<string>();
    for (const recipes of Object.values(data.categories)) {
      for (const recipe of recipes ?? []) {
        if (recipe.avoid_duplicates) types.add(recipe.type);
      }
    }
    return types;
  }, [data]);
}
