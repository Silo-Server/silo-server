import type {
  BrowseItem,
  CastMember,
  CatalogFiltersResponse,
  CrewMember,
  EpisodeFile,
  EpisodeListItem,
  FileVersion,
  ItemDetail,
  LeafItemUserData,
  LibraryCollection,
  LibraryTabCollection,
  LibraryTabGroup,
  LibraryTabResponse,
  LibraryTabUngrouped,
  MangaSeriesFiles,
  Person,
  PlaybackVariant,
  ResolvedSection,
  Season,
  SeasonUserData,
  SectionItem,
  SectionItemUpcomingEvent,
  ServerVisibleUserCollection,
  SubtitleInfo,
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

// ---------------------------------------------------------------------------
// Section catalog-items: item detail, children, files, people, and facets.
// The detail and list pages take the v1 shapes below; the adapters spell v2's
// absent members the way v1 did ("" / null) and turn string ids back into the
// numbers the components still compare against.
// ---------------------------------------------------------------------------

type CatalogItemDetailV2 = components["schemas"]["CatalogItemDetail"];
type CastCreditV2 = components["schemas"]["CastCredit"];
type CrewCreditV2 = components["schemas"]["CrewCredit"];
type EpisodeV2 = components["schemas"]["Episode"];
type EpisodeFileV2 = components["schemas"]["EpisodeFile"];
type SeasonV2 = components["schemas"]["Season"];
type FileVersionV2 = components["schemas"]["FileVersion"];
type PlaybackVariantV2 = components["schemas"]["PlaybackVariant"];
type SubtitleInfoV2 = components["schemas"]["SubtitleInfo"];
type WatchRollupV2 = components["schemas"]["WatchRollup"];
type MangaFilesV2 = components["schemas"]["MangaFiles"];
type PersonV2 = components["schemas"]["Person"];
type CatalogFiltersV2 = components["schemas"]["CatalogFilters"];

function watchRollupFromV2(rollup: WatchRollupV2): LeafItemUserData & SeasonUserData {
  return { ...rollup, last_file_id: optionalNumber(rollup.last_file_id) };
}

export function fileVersionFromV2(version: FileVersionV2): FileVersion {
  return { ...version, file_id: Number(version.file_id) };
}

function optionalNumber(value: string | undefined): number | undefined {
  return value === undefined ? undefined : Number(value);
}

function playbackVariantFromV2(variant: PlaybackVariantV2): PlaybackVariant {
  return {
    ...variant,
    default_file_id: optionalNumber(variant.default_file_id),
    parts: variant.parts.map((part) => ({
      ...part,
      default_file_id: optionalNumber(part.default_file_id),
      versions: part.versions.map(fileVersionFromV2),
    })),
  };
}

function subtitleInfoFromV2(subtitle: SubtitleInfoV2): SubtitleInfo {
  return { ...subtitle, codec: subtitle.codec ?? "", title: subtitle.title ?? "" };
}

function castMemberFromV2(credit: CastCreditV2): CastMember {
  return { ...credit, person_id: credit.person_id ?? "" };
}

function crewMemberFromV2(credit: CrewCreditV2): CrewMember {
  return { ...credit, person_id: credit.person_id ?? "" };
}

export function catalogItemDetailFromV2(item: CatalogItemDetailV2): ItemDetail {
  return {
    themes: item.themes,
    content_id: item.content_id,
    play_content_id: item.play_content_id,
    type: item.type as ItemDetail["type"],
    status: item.status ? (item.status as ItemDetail["status"]) : undefined,
    title: item.title,
    sort_title: item.sort_title,
    original_title: item.original_title,
    year: item.year ?? 0,
    overview: item.overview ?? "",
    tagline: item.tagline,
    pending_translation_language: item.pending_translation_language,
    runtime: item.runtime ?? 0,
    content_rating: item.content_rating ?? "",
    advisory_age: item.advisory_age ?? null,
    advisory_source: item.advisory_source ?? "",
    genres: item.genres,
    rating_imdb: item.rating_imdb ?? null,
    rating_tmdb: item.rating_tmdb ?? null,
    rating_rt_critic: item.rating_rt_critic ?? null,
    rating_rt_audience: item.rating_rt_audience ?? null,
    imdb_id: item.imdb_id ?? "",
    tmdb_id: item.tmdb_id ?? "",
    tvdb_id: item.tvdb_id ?? "",
    cast: item.cast.map(castMemberFromV2),
    crew: item.crew.map(crewMemberFromV2),
    studios: item.studios ?? [],
    networks: item.networks ?? [],
    countries: item.countries ?? [],
    locked_fields: item.locked_fields,
    release_date: item.release_date ?? null,
    first_air_date: item.first_air_date ?? null,
    show_status: item.show_status,
    last_air_date: item.last_air_date ?? null,
    air_time: item.air_time ?? null,
    air_timezone: item.air_timezone ?? null,
    poster_url: item.poster_url ?? "",
    poster_thumbhash: item.poster_thumbhash ?? "",
    backdrop_url: item.backdrop_url ?? "",
    backdrop_thumbhash: item.backdrop_thumbhash ?? "",
    logo_url: item.logo_url ?? "",
    season_count: item.season_count ?? null,
    series_id: item.series_id,
    series_title: item.series_title,
    season_number: item.season_number ?? null,
    episode_number: item.episode_number ?? null,
    episode_count: item.episode_count ?? null,
    air_date: item.air_date ?? null,
    is_specials: item.is_specials,
    user_data: item.user_data ? watchRollupFromV2(item.user_data) : undefined,
    user_state: item.user_state,
    user_rating: item.user_rating ?? null,
    folder_paths: item.folder_paths,
    videos: item.videos,
    extras: item.extras,
    versions: item.versions.map(fileVersionFromV2),
    playback_variants: item.playback_variants?.map(playbackVariantFromV2),
    subtitles: item.subtitles.map(subtitleInfoFromV2),
    intro: item.intro ?? null,
    credits: item.credits ?? null,
    recap: item.recap ?? null,
    preview: item.preview ?? null,
    effective_subtitle_language: item.effective_subtitle_language,
    effective_subtitle_mode: item.effective_subtitle_mode,
    effective_show_forced_subtitles: item.effective_show_forced_subtitles,
    effective_subtitle_track_signature: item.effective_subtitle_track_signature,
    effective_version_resolution: item.effective_version_resolution,
    effective_version_hdr: item.effective_version_hdr,
    effective_version_codec_video: item.effective_version_codec_video,
    effective_version_edition_key: item.effective_version_edition_key,
    audiobook: item.audiobook,
    ebook: item.ebook,
    manga: item.manga,
  };
}

function episodeFileFromV2(file: EpisodeFileV2): EpisodeFile {
  return {
    file_id: Number(file.file_id),
    resolution: file.resolution ?? "",
    codec_video: file.codec_video ?? "",
    hdr: file.hdr,
    audio_channels: file.audio_channels ?? 0,
    container: file.container ?? "",
    file_size: file.file_size,
  };
}

export function episodeFromV2(episode: EpisodeV2): EpisodeListItem {
  return {
    content_id: episode.content_id,
    season_number: episode.season_number,
    episode_number: episode.episode_number,
    title: episode.title,
    overview: episode.overview ?? "",
    air_date: episode.air_date ?? null,
    runtime: episode.runtime,
    imdb_id: episode.imdb_id,
    tmdb_id: episode.tmdb_id,
    tvdb_id: episode.tvdb_id,
    still_url: episode.still_url ?? "",
    still_thumbhash: episode.still_thumbhash ?? "",
    user_data: episode.user_data ? watchRollupFromV2(episode.user_data) : undefined,
    files: (episode.files ?? []).map(episodeFileFromV2),
    overlay_summary: episode.overlay_summary,
  };
}

export function seasonFromV2(season: SeasonV2): Season {
  return {
    content_id: season.content_id,
    play_content_id: season.play_content_id,
    season_number: season.season_number,
    is_specials: season.is_specials ?? false,
    title: season.title,
    overview: season.overview ?? "",
    air_date: season.air_date ?? null,
    episode_count: season.episode_count,
    poster_url: season.poster_url ?? "",
    poster_thumbhash: season.poster_thumbhash ?? "",
    user_data: season.user_data ? watchRollupFromV2(season.user_data) : undefined,
  };
}

export function mangaFilesFromV2(files: MangaFilesV2): MangaSeriesFiles {
  return { folder_paths: files.folder_paths, files: files.items };
}

export function personFromV2(person: PersonV2): Person {
  return { ...person };
}

export function catalogFiltersFromV2(filters: CatalogFiltersV2): CatalogFiltersResponse {
  const { technical, ...facets } = filters;
  return {
    ...facets,
    resolutions: technical?.resolutions,
    audio_languages: technical?.audio_languages,
    subtitle_languages: technical?.subtitle_languages,
  };
}
