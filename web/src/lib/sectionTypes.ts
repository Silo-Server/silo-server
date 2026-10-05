export const FILTER_SECTION_TYPES = new Set(["genre", "custom_filter"]);

/**
 * Mirrors the save gate: the server refuses admin-only recipes from a
 * non-admin account unless profiles may build custom sections. An unloaded
 * flag counts as not allowed.
 */
export function canAddAdminOnlyRecipes(
  role: string | undefined,
  allowProfileCustomSections: boolean | undefined,
): boolean {
  return role === "admin" || allowProfileCustomSections === true;
}

/**
 * Whether a section config names Trakt as its source, read the way the
 * server's section source policy reads it. New Trakt-backed overrides are
 * refused, and legacy Trakt admin sections can be hidden but never changed
 * or shown again.
 */
export function isTraktConfig(config: Record<string, unknown> | undefined): boolean {
  return config?.source === "trakt" || config?.source_provider === "trakt";
}
