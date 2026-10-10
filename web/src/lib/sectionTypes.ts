export const FILTER_SECTION_TYPES = new Set(["genre", "custom_filter"]);

/**
 * Whether a section config names Trakt as its source, read the way the
 * server's section source policy reads it. New Trakt-backed overrides are
 * refused, and legacy Trakt admin sections can be hidden but never changed
 * or shown again.
 */
export function isTraktConfig(config: Record<string, unknown> | undefined): boolean {
  return config?.source === "trakt" || config?.source_provider === "trakt";
}
