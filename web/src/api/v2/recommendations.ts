import type { DiscoverResponse, DiscoverRow, RecommendationSectionResponse } from "@/api/types";
import { catalogItemFromV2 } from "@/api/v2/catalog";
import type { components } from "@/api/v2/schema";

/**
 * Recommendation reads as the Discover, For You, and Watch Tonight screens
 * still model them. v2 renders every recommended item as the shared
 * CatalogItem and names a row's members `title`/`kind`/`key`; the adapters
 * below keep the v1 hook return shapes so the screens are untouched.
 */

type RecommendationRowV2 = components["schemas"]["RecommendationRow"];
type WatchTonightV2 = components["schemas"]["WatchTonight"];
type WatchTonightItemV2 = components["schemas"]["WatchTonightItem"];
type WatchTonightCardV2 = components["schemas"]["WatchTonightCard"];
type WatchTonightCardPageV2 = components["schemas"]["WatchTonightCardPage"];

export type WatchTonightSource = WatchTonightItemV2["watch_tonight_source"];

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

export function watchTonightItemFromV2(item: WatchTonightItemV2) {
  return { ...catalogItemFromV2(item), watch_tonight_source: item.watch_tonight_source };
}

export function watchTonightFromV2(body: WatchTonightV2) {
  return { items: body.items.map(watchTonightItemFromV2), is_cold: body.is_cold };
}

export function swipeCardFromV2(card: WatchTonightCardV2) {
  return { ...watchTonightItemFromV2(card), cast: card.cast };
}

export function swipeCardsPageFromV2(page: WatchTonightCardPageV2) {
  return { cards: page.items.map(swipeCardFromV2), has_more: page.has_more, is_cold: page.is_cold };
}
