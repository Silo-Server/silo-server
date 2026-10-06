// Library filter for home-screen sections whose backend honors
// filter_library_ids (Recently Added, Recently Released). The backend reads the
// flat filter_library_ids / filter_library_id keys first and falls back to the
// query-definition library_ids only when neither flat key is set (see
// ParseConfigFilters in internal/sections/types.go); these helpers keep the same
// precedence so the editor shows and writes what the server will use.

export const LIBRARY_FILTER_SECTION_TYPES = new Set(["recently_added", "recently_released"]);

function positiveIds(value: unknown): number[] {
  if (!Array.isArray(value)) return [];
  const ids: number[] = [];
  for (const id of value) {
    if (typeof id === "number" && Number.isInteger(id) && id > 0 && !ids.includes(id)) {
      ids.push(id);
    }
  }
  return ids;
}

function flatFilterIds(config: Record<string, unknown>): number[] | null {
  // Like the backend, any non-empty filter_library_ids array counts as set,
  // even when none of its entries is a usable ID.
  const hasList = Array.isArray(config.filter_library_ids) && config.filter_library_ids.length > 0;
  const ids = positiveIds(config.filter_library_ids);
  const single = config.filter_library_id;
  const hasSingle = typeof single === "number" && Number.isInteger(single);
  if (!hasList && !hasSingle) return null;
  if (hasSingle && single > 0 && !ids.includes(single)) ids.push(single);
  return ids;
}

function usesQueryDefinitionShape(config: Record<string, unknown>): boolean {
  return Array.isArray(config.library_ids) || typeof config.media_scope === "string";
}

/** Returns the library IDs the backend filters a section to, or [] for all libraries. */
export function sectionLibraryFilterIds(config: Record<string, unknown>): number[] {
  return flatFilterIds(config) ?? positiveIds(config.library_ids);
}

// The library a generated row belongs to, read in the backend's order
// (generated_library_id, then filter_library_id; see
// parseGeneratedHomeLibraryRecentConfig in internal/sections/generated.go).
function generatedOwnerId(config: Record<string, unknown>): number | null {
  if (typeof config.generated_source !== "string" || config.generated_source === "") return null;
  for (const id of [config.generated_library_id, config.filter_library_id]) {
    if (typeof id === "number" && Number.isInteger(id) && id > 0) return id;
  }
  return null;
}

/**
 * Returns a draft config filtered to libraryIds ([] means all libraries).
 * A query-definition config keeps its shape so its media_scope still applies;
 * any other config is written to filter_library_ids, and the legacy
 * filter_library_id is removed so it cannot widen the new selection.
 *
 * Keep the generated owner throughout the draft so a temporary deselection can
 * be undone. finalizeSectionLibraryFilter decides ownership when saving.
 */
export function withSectionLibraryFilterIds(
  config: Record<string, unknown>,
  libraryIds: number[],
): Record<string, unknown> {
  const ids = positiveIds(libraryIds);
  const next = { ...config };
  const owner = generatedOwnerId(config);
  if (owner !== null) {
    next.generated_library_id = owner;
  }
  if (flatFilterIds(config) === null && usesQueryDefinitionShape(config)) {
    if (ids.length > 0) next.library_ids = ids;
    else delete next.library_ids;
    return next;
  }
  delete next.filter_library_id;
  if (ids.length > 0) {
    next.filter_library_ids = ids;
  } else {
    // Without a flat key the backend falls back to library_ids, so a stale
    // one would keep the row filtered.
    delete next.filter_library_ids;
    delete next.library_ids;
  }
  return next;
}

/** Detaches a generated row only when its saved selection excludes its library. */
export function finalizeSectionLibraryFilter(
  config: Record<string, unknown>,
): Record<string, unknown> {
  const owner = generatedOwnerId(config);
  const ids = sectionLibraryFilterIds(config);
  return owner !== null && ids.length > 0 && !ids.includes(owner)
    ? { ...config, generated_library_id: null }
    : { ...config };
}
