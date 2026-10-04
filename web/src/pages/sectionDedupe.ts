import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type { ResolvedSection, ResolvedSectionLayout } from "@/api/types";
import { fetchRecipeCatalog } from "@/lib/recipes";

const NO_TYPES: ReadonlySet<string> = new Set();

/**
 * dropCrossSectionDuplicates applies the recipes' avoid_duplicates rule to
 * sections that load one request at a time, as the server does for the
 * aggregate sections endpoint. Sections are visited in layout order; each
 * marks its items seen, and a section whose type avoids duplicates first
 * drops the items an earlier section shows. Rows arrive out of order, so
 * callers rerun this over every loaded section whenever one arrives: the
 * result then depends only on what has loaded, not on arrival order.
 * Unchanged sections keep their identity.
 */
export function dropCrossSectionDuplicates(
  layout: readonly ResolvedSectionLayout[],
  loadedSections: ReadonlyMap<string, ResolvedSection>,
  avoidDuplicateTypes: ReadonlySet<string>,
): Map<string, ResolvedSection> {
  const result = new Map(loadedSections);
  if (avoidDuplicateTypes.size === 0) return result;

  const seen = new Set<string>();
  for (const entry of layout) {
    const section = loadedSections.get(entry.id);
    if (!section) continue;

    let visible = section;
    if (avoidDuplicateTypes.has(section.section_type)) {
      const kept = section.items.filter((item) => !item.content_id || !seen.has(item.content_id));
      const dropped = section.items.length - kept.length;
      if (dropped > 0) {
        visible = {
          ...section,
          items: kept,
          total_count: Math.max(kept.length, section.total_count - dropped),
        };
        result.set(entry.id, visible);
      }
    }
    for (const item of visible.items) {
      if (item.content_id) seen.add(item.content_id);
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
): Map<string, ResolvedSection> {
  const avoidDuplicateTypes = useAvoidDuplicateSectionTypes();
  return useMemo(
    () => dropCrossSectionDuplicates(layout, loadedSections, avoidDuplicateTypes),
    [avoidDuplicateTypes, layout, loadedSections],
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
