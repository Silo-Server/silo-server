import type {
  FileVersion,
  LeafItemUserData,
  PlaybackVariant,
  PlaybackVariantPart,
  TimeRange,
  WatchDetail,
} from "@/api/types";
import type { components } from "@/api/v2/schema";

/**
 * The watch detail as the player still models it. v2 renders file ids as
 * string IDs, durations as `*_seconds`, markers as `{start_seconds,
 * end_seconds}` and `added_at` as an instant; the player, the version picker
 * and the playback session bridge take the v1 `WatchDetail`, so the adapter
 * converts at the boundary and those stay untouched.
 */

type WatchDetailV2 = components["schemas"]["WatchDetail"];
type WatchFileVersionV2 = components["schemas"]["WatchFileVersion"];
type WatchMarkerV2 = components["schemas"]["WatchMarker"];
type WatchPlaybackVariantV2 = components["schemas"]["WatchPlaybackVariant"];
type WatchPlaybackVariantPartV2 = components["schemas"]["WatchPlaybackVariantPart"];
type WatchUserDataV2 = components["schemas"]["WatchUserData"];

function numericId(id: string | undefined): number | undefined {
  if (id === undefined || id === "") return undefined;
  const n = Number(id);
  return Number.isFinite(n) ? n : undefined;
}

function markerFromV2(marker: WatchMarkerV2 | undefined): TimeRange | null {
  if (!marker) return null;
  return { start: marker.start_seconds, end: marker.end_seconds };
}

function optionalMarkerFromV2(marker: WatchMarkerV2 | undefined): TimeRange | undefined {
  return marker ? { start: marker.start_seconds, end: marker.end_seconds } : undefined;
}

function fileVersionFromV2(version: WatchFileVersionV2): FileVersion {
  const {
    file_id,
    duration_seconds,
    intro,
    credits,
    recap,
    preview,
    video_tracks,
    audio_tracks,
    subtitle_tracks,
    chapters,
    ...rest
  } = version;
  return {
    ...rest,
    file_id: numericId(file_id) ?? 0,
    duration: duration_seconds,
    intro: optionalMarkerFromV2(intro),
    credits: optionalMarkerFromV2(credits),
    recap: optionalMarkerFromV2(recap),
    preview: optionalMarkerFromV2(preview),
    video_tracks,
    audio_tracks,
    subtitle_tracks,
    chapters,
  };
}

function variantPartFromV2(part: WatchPlaybackVariantPartV2): PlaybackVariantPart {
  return {
    part_index: part.part_index,
    default_file_id: numericId(part.default_file_id),
    total_duration: part.total_duration_seconds,
    versions: part.versions.map(fileVersionFromV2),
  };
}

function variantFromV2(variant: WatchPlaybackVariantV2): PlaybackVariant {
  const { default_file_id, total_duration_seconds, parts, ...rest } = variant;
  return {
    ...rest,
    default_file_id: numericId(default_file_id),
    total_duration: total_duration_seconds,
    parts: parts.map(variantPartFromV2),
  };
}

function userDataFromV2(data: WatchUserDataV2 | undefined): LeafItemUserData | undefined {
  if (!data) return undefined;
  return {
    played: data.played,
    is_in_progress: data.is_in_progress,
    position_seconds: data.position_seconds,
    duration_seconds: data.duration_seconds,
    last_file_id: numericId(data.last_file_id),
    last_resolution: data.last_resolution,
    last_hdr: data.last_hdr,
    last_codec_video: data.last_codec_video,
    last_edition_key: data.last_edition_key,
  };
}

export function watchDetailFromV2(detail: WatchDetailV2): WatchDetail {
  return {
    content_id: detail.content_id,
    type: detail.type,
    title: detail.title,
    year: detail.year,
    overview: detail.overview ?? "",
    versions: detail.versions.map(fileVersionFromV2),
    playback_variants: detail.playback_variants?.map(variantFromV2),
    subtitles: detail.subtitles.map((s) => ({
      source: s.source,
      language: s.language,
      codec: s.codec ?? "",
      forced: s.forced,
      hearing_impaired: s.hearing_impaired,
      title: s.title ?? "",
    })),
    intro: markerFromV2(detail.intro),
    credits: markerFromV2(detail.credits),
    recap: markerFromV2(detail.recap),
    preview: markerFromV2(detail.preview),
    user_data: userDataFromV2(detail.user_data),
    series_id: detail.series_id,
    series_title: detail.series_title,
    season_number: detail.season_number,
    episode_number: detail.episode_number,
    effective_subtitle_language: detail.effective_subtitle_language,
    effective_subtitle_mode: detail.effective_subtitle_mode,
    effective_show_forced_subtitles: detail.effective_show_forced_subtitles,
    effective_subtitle_track_signature: detail.effective_subtitle_track_signature,
    effective_version_resolution: detail.effective_version_resolution,
    effective_version_hdr: detail.effective_version_hdr,
    effective_version_codec_video: detail.effective_version_codec_video,
    effective_version_edition_key: detail.effective_version_edition_key,
  };
}
