import type {
  QueryDefinition,
  QueryRule,
  UserCollectionMediaFilter,
  UserCollectionWatchFilter,
} from "@/api/types";

export const COLLECTION_WATCH_FILTER_OPTIONS: Array<{
  value: UserCollectionWatchFilter;
  label: string;
}> = [
  { value: "all", label: "All" },
  { value: "unwatched", label: "Unwatched" },
  { value: "watched", label: "Watched" },
];

export const COLLECTION_MEDIA_FILTER_OPTIONS: Array<{
  value: UserCollectionMediaFilter;
  label: string;
}> = [
  { value: "all", label: "All" },
  { value: "movie", label: "Movies" },
  { value: "series", label: "Shows" },
];

export function collectionWatchFilterLabel(value: UserCollectionWatchFilter): string {
  return COLLECTION_WATCH_FILTER_OPTIONS.find((option) => option.value === value)?.label ?? "All";
}

export function collectionMediaFilterLabel(value: UserCollectionMediaFilter): string {
  return COLLECTION_MEDIA_FILTER_OPTIONS.find((option) => option.value === value)?.label ?? "All";
}

// The display filters are persisted as a filter-only QueryDefinition fragment:
// a single AND group holding the watched / type rules. It intentionally omits
// library_ids / media_scope / sort / limit (those are owned elsewhere), so the
// fragment is built and read with the helpers below rather than the generic
// normalizeQueryDefinition (which would inject those fields back in).

/**
 * Build the filter-only QueryDefinition fragment for the two display presets.
 * Returns undefined when both presets are "all" (i.e. no display filter).
 */
export function displayFiltersToQueryDefinition(
  watch: UserCollectionWatchFilter,
  media: UserCollectionMediaFilter,
): QueryDefinition | undefined {
  const rules: QueryRule[] = [];
  if (watch === "watched") {
    rules.push({ field: "watched", op: "is", value: true });
  } else if (watch === "unwatched") {
    rules.push({ field: "watched", op: "is", value: false });
  }
  if (media === "movie") {
    rules.push({ field: "type", op: "is", value: "movie" });
  } else if (media === "series") {
    rules.push({ field: "type", op: "is", value: "series" });
  }
  if (rules.length === 0) {
    return undefined;
  }
  // Filter-only fragment: only match + a single AND group. library_ids /
  // media_scope / sort / limit are intentionally absent.
  return {
    match: "all",
    groups: [{ match: "all", rules }],
  } as QueryDefinition;
}

/**
 * Read the two display presets back out of a filter-only fragment. Tolerant of
 * rule order, missing groups, and an absent fragment; each preset defaults to
 * "all" when its rule is not present.
 */
export function queryDefinitionToDisplayFilters(def: QueryDefinition | undefined | null): {
  watch: UserCollectionWatchFilter;
  media: UserCollectionMediaFilter;
} {
  let watch: UserCollectionWatchFilter = "all";
  let media: UserCollectionMediaFilter = "all";
  if (!def || !Array.isArray(def.groups)) {
    return { watch, media };
  }
  for (const group of def.groups) {
    for (const rule of group?.rules ?? []) {
      if (rule.field === "watched" && rule.op === "is") {
        if (rule.value === true) {
          watch = "watched";
        } else if (rule.value === false) {
          watch = "unwatched";
        }
      } else if (rule.field === "type" && rule.op === "is") {
        if (rule.value === "movie") {
          media = "movie";
        } else if (rule.value === "series") {
          media = "series";
        }
      }
    }
  }
  return { watch, media };
}
