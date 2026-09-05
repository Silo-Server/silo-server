import type {
  BrowseItem,
  LibraryCollection,
  LibraryTabCollection,
  LibraryTabGroup,
  LibraryTabResponse,
  LibraryTabUngrouped,
  ResolvedSection,
  SectionItem,
  SectionItemUpcomingEvent,
  ServerVisibleUserCollection,
} from "@/api/types";
import type { components } from "@/api/v2/schema";

/**
 * Catalog item cards as the app still models them. v2 emits one shared
 * `CatalogItem` summary for every card surface (sections, collections, browse);
 * the card components take the v1 `SectionItem` / `BrowseItem` shapes, which
 * spell absent strings as "" and absent ratings as null. The adapters below
 * convert at the boundary so the components stay untouched.
 */

type CatalogItemV2 = components["schemas"]["CatalogItem"];
type SectionV2 = components["schemas"]["Section"];
type CuratedCollectionV2 = components["schemas"]["CuratedCollection"];
type LibraryCollectionCardV2 = components["schemas"]["LibraryCollectionCard"];
type LibraryCollectionGroupV2 = components["schemas"]["LibraryCollectionGroup"];
type LibraryCollectionTabV2 = components["schemas"]["LibraryCollectionTab"];
type UserCollectionV2 = components["schemas"]["UserCollection"];

/** A card that satisfies both the section and browse card component props. */
export type CatalogCardItem = SectionItem & BrowseItem;

export function catalogItemFromV2(item: CatalogItemV2): CatalogCardItem {
  return {
    content_id: item.content_id,
    play_content_id: item.play_content_id,
    type: item.type as CatalogCardItem["type"],
    title: item.title,
    series_id: item.series_id,
    series_title: item.series_title,
    season_number: item.season_number,
    episode_number: item.episode_number,
    year: item.year ?? 0,
    runtime: item.runtime,
    genres: item.genres,
    studios: item.studios,
    networks: item.networks,
    content_rating: item.content_rating ?? "",
    status: item.status as CatalogCardItem["status"],
    show_status: item.show_status,
    rating_imdb: item.rating_imdb ?? null,
    rating_tmdb: item.rating_tmdb,
    rating_rt_critic: item.rating_rt_critic,
    rating_rt_audience: item.rating_rt_audience,
    original_language: item.original_language,
    overview: item.overview ?? "",
    item_source: item.item_source,
    position_seconds: item.position_seconds,
    duration_seconds: item.duration_seconds,
    progress_updated_at: item.progress_updated_at,
    poster_url: item.poster_url ?? "",
    poster_thumbhash: item.poster_thumbhash ?? "",
    backdrop_url: item.backdrop_url ?? "",
    backdrop_thumbhash: item.backdrop_thumbhash ?? "",
    logo_url: item.logo_url ?? "",
    added_at: item.added_at,
    release_date: item.release_date,
    last_air_date: item.last_air_date,
    overlay_summary: item.overlay_summary,
    sort_metrics: item.sort_metrics,
    badges: item.badges,
    user_state: item.user_state,
    upcoming_event: item.upcoming_event
      ? {
          ...item.upcoming_event,
          type: item.upcoming_event.type as SectionItemUpcomingEvent["type"],
        }
      : undefined,
    manga_chapter_count: item.manga_chapter_count,
    manga_volume_count: item.manga_volume_count,
  };
}

export function sectionFromV2(section: SectionV2): ResolvedSection {
  return {
    id: section.id,
    section_type: section.section_type,
    title: section.title,
    featured: section.featured,
    item_limit: section.item_limit,
    total_count: section.total_count,
    is_custom: section.is_custom,
    customized: section.customized,
    items: section.items.map(catalogItemFromV2),
  };
}

function recordOrEmpty(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return {};
  return value as Record<string, unknown>;
}

export function libraryCollectionFromV2(collection: CuratedCollectionV2): LibraryCollection {
  return {
    id: collection.id,
    library_id: Number(collection.library_id),
    library_ids: collection.library_ids.map(Number),
    slug: collection.slug,
    title: collection.title,
    description: collection.description,
    collection_type: collection.collection_type as LibraryCollection["collection_type"],
    visibility: collection.visibility as LibraryCollection["visibility"],
    sort_order: collection.sort_order,
    group_id: collection.group_id,
    featured: collection.featured,
    poster_url: collection.poster_url,
    backdrop_url: collection.backdrop_url,
    poster_thumbhash: collection.poster_thumbhash,
    backdrop_thumbhash: collection.backdrop_thumbhash,
    source_url: collection.source_url,
    query_definition: recordOrEmpty(
      collection.query_definition,
    ) as unknown as LibraryCollection["query_definition"],
    sort_config: recordOrEmpty(collection.sort_config),
    source_config: recordOrEmpty(collection.source_config),
    management_mode: collection.management_mode as LibraryCollection["management_mode"],
    management_source: collection.management_source,
    management_key: collection.management_key,
    last_sync_status: collection.last_sync_status as LibraryCollection["last_sync_status"],
    last_sync_message: collection.last_sync_message,
    last_sync_at: collection.last_sync_at,
    sync_schedule: collection.sync_schedule,
    next_sync_at: collection.next_sync_at,
    item_count: collection.item_count,
    created_at: collection.created_at,
    updated_at: collection.updated_at,
  };
}

function libraryCollectionCardFromV2(card: LibraryCollectionCardV2): LibraryTabCollection {
  return {
    id: card.id,
    title: card.title,
    poster_url: card.poster_url,
    poster_thumbhash: card.poster_thumbhash,
    item_count: card.item_count,
    featured: card.featured,
    creator_profile_id: card.creator_profile_id,
  };
}

function libraryCollectionGroupFromV2(group: LibraryCollectionGroupV2): LibraryTabGroup {
  return {
    id: group.id,
    name: group.name,
    kind: group.kind as LibraryTabGroup["kind"],
    sort_mode: group.sort_mode as LibraryTabGroup["sort_mode"],
    sort_order: group.sort_order,
    collections: group.collections.map(libraryCollectionCardFromV2),
  };
}

export function libraryCollectionTabFromV2(tab: LibraryCollectionTabV2): LibraryTabResponse {
  const ungrouped: LibraryTabUngrouped | undefined = tab.ungrouped
    ? {
        sort_order: tab.ungrouped.sort_order,
        collections: tab.ungrouped.collections.map(libraryCollectionCardFromV2),
      }
    : undefined;
  return {
    library_id: Number(tab.library_id),
    collections: tab.collections.map(libraryCollectionFromV2),
    groups: tab.groups.map(libraryCollectionGroupFromV2),
    ungrouped,
  };
}

export function userCollectionFromV2(collection: UserCollectionV2): ServerVisibleUserCollection {
  return {
    ...collection,
    collection_type: collection.collection_type as ServerVisibleUserCollection["collection_type"],
  };
}
