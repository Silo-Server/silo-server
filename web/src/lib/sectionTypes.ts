import type { Category, RecipeCatalogResponse } from "@/lib/recipes";

export const SECTION_TYPES = [
  { value: "recently_added", label: "Recently Added" },
  { value: "recently_released", label: "Recently Released" },
  { value: "genre", label: "Genre" },
  { value: "custom_filter", label: "Custom Filter" },
  { value: "random", label: "Random" },
  { value: "continue_watching", label: "Continue Watching" },
  { value: "recommended_for_you", label: "Recommended For You" },
  { value: "because_you_watched", label: "Because You Watched" },
  { value: "similar_users_liked", label: "Profiles Like You Enjoyed" },
  { value: "taste_match", label: "Top Picks Today" },
  { value: "next_up", label: "On Deck" },
  { value: "next_in_series", label: "Next in Series" },
  { value: "watchlist", label: "Watchlist" },
  { value: "favorites", label: "Favorites" },
  { value: "collection", label: "Collection" },
];

export const FILTER_SECTION_TYPES = new Set(["genre", "custom_filter"]);

export function sectionTypeLabel(type: string): string {
  return SECTION_TYPES.find((t) => t.value === type)?.label ?? type;
}

/** The static fallback types a profile may pick from; custom_filter is admin-only. */
export function fallbackSectionTypes(allowAdminOnly: boolean) {
  return allowAdminOnly
    ? SECTION_TYPES
    : SECTION_TYPES.filter((type) => type.value !== "custom_filter");
}

/**
 * Drops admin-only recipes from the catalog a profile may pick from, so a
 * profile the server would refuse (403 custom_disabled) is not offered them.
 */
export function filterRecipeCatalog(
  catalog: RecipeCatalogResponse | undefined,
  allowAdminOnly: boolean,
): RecipeCatalogResponse | undefined {
  if (!catalog || allowAdminOnly) return catalog;
  const categories: RecipeCatalogResponse["categories"] = {};
  for (const category of Object.keys(catalog.categories) as Category[]) {
    categories[category] = (catalog.categories[category] ?? []).filter(
      (definition) => !definition.admin_only,
    );
  }
  return { categories };
}
