import type { BrowseItem, EpisodeListItem, OverlaySummary, SectionItem } from "@/api/types";
import type { WatchlistTitle } from "@/api/v2/watchlistTitles";
import type { WatchlistTitleStatus } from "@/lib/watchlistTitles";
import type { OverlayData } from "./types";

// BrowseItem and SectionItem share the fields the overlay system consumes;
// the structural type below is the intersection actually used. The shared
// extractor centralizes the mapping so adding a new overlay-relevant field
// only touches one place.
interface OverlaySourceItem {
  overlay_summary?: OverlaySummary | null;
  rating_imdb?: number | null;
  rating_tmdb?: number | null;
  rating_rt_critic?: number | null;
  rating_rt_audience?: number | null;
  content_rating?: string;
  advisory_age?: number | null;
  year?: number | null;
  runtime?: number;
  original_language?: string;
  studios?: string[];
  networks?: string[];
  show_status?: string;
}

function firstNonEmpty(values: string[] | undefined): string | undefined {
  if (!values) return undefined;
  for (const v of values) {
    const trimmed = v?.trim();
    if (trimmed) return trimmed;
  }
  return undefined;
}

function extract(item: OverlaySourceItem): OverlayData {
  const summary = item.overlay_summary ?? undefined;
  return {
    resolution: summary?.resolution,
    hdr: summary?.hdr,
    audio: summary?.audio,
    audio_channels: summary?.audio_channels,
    video_codec: summary?.video_codec,
    container: summary?.container,
    aspect_ratio: summary?.aspect_ratio,
    release_type: summary?.release_type,
    edition: summary?.edition,
    multi_audio: summary?.multi_audio,
    multi_sub: summary?.multi_sub,
    rating_imdb: item.rating_imdb,
    rating_tmdb: item.rating_tmdb,
    rating_rt_critic: item.rating_rt_critic,
    rating_rt_audience: item.rating_rt_audience,
    content_rating: item.content_rating || undefined,
    advisory_age: item.advisory_age ?? null,
    year: item.year || null,
    runtime: item.runtime ?? null,
    original_language: item.original_language,
    studio: firstNonEmpty(item.studios),
    network: firstNonEmpty(item.networks),
    show_status: item.show_status || undefined,
    // imdb_top_250 / rt_certified_fresh are not yet on either API shape —
    // they stay null until backend data sources land.
    imdb_top_250: null,
    rt_certified_fresh: null,
  };
}

export function overlayDataFromBrowseItem(item: BrowseItem): OverlayData {
  return extract(item);
}

export function overlayDataFromSectionItem(item: SectionItem): OverlayData {
  return extract(item);
}

export function overlayDataFromEpisodeListItem(item: EpisodeListItem): OverlayData {
  return extract(item);
}

/**
 * A watchlist title the library doesn't have. TMDB supplies the rating, age
 * rating and year; file-based badges (resolution, HDR, audio) have no value
 * and don't render. The request_status badge takes the status the card
 * derived (watchlistTitleStatus), which keeps the request labels out of the
 * overlay library.
 */
export function overlayDataFromWatchlistTitle(
  title: Pick<WatchlistTitle, "vote_average" | "content_rating" | "year">,
  status: Pick<WatchlistTitleStatus, "badge" | "badgeIcon" | "attention" | "downloadPercent">,
): OverlayData {
  return {
    rating_tmdb: title.vote_average ?? null,
    content_rating: title.content_rating || undefined,
    year: title.year || null,
    request_status: status.badge,
    request_status_icon: status.badgeIcon,
    request_status_attention: status.attention,
    request_download_percent: status.downloadPercent ?? null,
  };
}
