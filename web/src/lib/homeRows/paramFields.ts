import { LIBRARY_FILTER_SECTION_TYPES } from "@/lib/sectionLibraryFilter";
import { variantOf } from "./variants";

type Config = Record<string, unknown>;

/**
 * The config keys each kind's fields edit, per slot. VariantChoice owns the
 * variant keys; the only overlaps are the holiday list, which refines the
 * Holidays variant, and a spotlight's subject (a test pins both).
 */
const FIELD_KEYS: Record<string, { primary?: string[]; more?: string[] }> = {
  recently_added: { primary: ["filter_library_ids", "filter_library_id", "library_ids"] },
  recently_released: { primary: ["filter_library_ids", "filter_library_id", "library_ids"] },
  watchlist: { more: ["filter_type", "filter_library_ids", "sort", "order"] },
  favorites: { more: ["filter_type", "filter_library_ids", "sort", "order"] },
  returning_shows: { more: ["lookback_days"] },
  short_watches: { more: ["max_minutes"] },
  anniversaries: { more: ["milestone_years"] },
  taste_match: { more: ["genre"] },
  editorial_spotlight: { more: ["auto_rotate", "rotation_cadence", "subject"] },
  seasonal_themed: { primary: ["enabled_themes", "theme_titles", "theme", "mode"] },
};

export function paramFieldKeys(sectionType: string): string[] {
  const keys = FIELD_KEYS[sectionType];
  return [...(keys?.primary ?? []), ...(keys?.more ?? [])];
}

/** Whether this kind has fields in a slot on this page. */
export function hasParamFields(
  sectionType: string,
  slot: "primary" | "more",
  config: Config,
  onLibraryPage: boolean,
): boolean {
  if (slot === "primary") {
    if (LIBRARY_FILTER_SECTION_TYPES.has(sectionType)) return !onLibraryPage;
    if (sectionType === "seasonal_themed") {
      // Holidays, or a legacy single-holiday row, whose list starts from that holiday.
      const variant = variantOf(sectionType, config);
      return variant === "se_auto" || (variant === null && typeof config.theme === "string");
    }
  }
  return Boolean(FIELD_KEYS[sectionType]?.[slot]);
}
