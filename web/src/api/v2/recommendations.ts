import { catalogItemFromV2, type CatalogCardItem } from "@/api/v2/catalog";
import type { components } from "@/api/v2/schema";

/**
 * Recommendation reads as the Discover and For You screens still model them.
 * v2 renders every recommended item as the shared CatalogItem and names a
 * row's members `title`/`kind`/`key`; the adapters below keep the v1 hook
 * return shapes so the screens are untouched.
 */

type RecommendationRowV2 = components["schemas"]["RecommendationRow"];

/** A recommendation row as the Discover and For You screens render it. */
export interface DiscoverRow {
  type: string;
  label: string;
  /** URL kind for the dedicated "see all" page (e.g. "for-you-main", "cluster", "genre"). */
  section_kind?: string;
  /** URL key paired with section_kind when needed (cluster index or genre name). */
  section_key?: string;
  items: CatalogCardItem[];
}

export interface DiscoverResponse {
  rows: DiscoverRow[];
}

export interface RecommendationSectionResponse {
  kind: string;
  key?: string;
  type: string;
  label: string;
  items: CatalogCardItem[];
}

export function discoverRowFromV2(row: RecommendationRowV2): DiscoverRow {
  return {
    type: row.type,
    label: row.title,
    section_kind: row.kind,
    section_key: row.key,
    items: row.items.map(catalogItemFromV2),
  };
}

export function discoverFromV2(rows: { items: RecommendationRowV2[] }): DiscoverResponse {
  return { rows: rows.items.map(discoverRowFromV2) };
}

export function recommendationSectionFromV2(
  row: RecommendationRowV2,
): RecommendationSectionResponse {
  return {
    kind: row.kind ?? "",
    key: row.key,
    type: row.type,
    label: row.title,
    items: row.items.map(catalogItemFromV2),
  };
}
