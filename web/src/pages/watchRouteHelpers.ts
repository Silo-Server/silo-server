import type { Profile, WatchDetail } from "@/api/types";
import type {
  EpisodeRef,
  IntroSkipMode,
  PrePlaySubtitleSelection,
  PlayerSubtitleInfo,
  PlayerSubtitleTrackSignature,
  PlayerTimeRange,
  ResumeHints,
  SubtitleMode,
  WatchPageProps,
} from "@/player";
import { resolveVersionAudioLanguage } from "@/player/utils/effectiveAudioLanguage";
import { isBitmapCodec } from "@/player/utils/subtitleCodecs";
import { resolveSubtitleAutoSelect } from "@/player/utils/subtitleSort";

export interface WatchRouteRequest {
  contentId: string;
  fileId?: number;
  libraryId?: number;
  roomId?: string;
  roomToken?: string;
  restart: boolean;
  audioTrackIndex?: number;
  prePlaySubtitleMode?: "auto" | "off" | "explicit";
  prePlaySubtitleSelection?: PrePlaySubtitleSelection | null;
  returnHref?: string;
  requestKey: string;
}

export interface WatchPlaybackStartInput {
  contentId: string;
  fileId?: number;
  libraryId?: number;
  roomId?: string;
  roomToken?: string;
  restart?: boolean;
  audioTrackIndex?: number;
  prePlaySubtitleMode?: "auto" | "off" | "explicit";
  prePlaySubtitleSelection?: PrePlaySubtitleSelection | null;
  returnHref?: string;
}

function parseOptionalInt(value: string | null): number | undefined {
  if (!value) return undefined;

  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) ? parsed : undefined;
}

function buildWatchRouteRequestKey(
  contentId: string,
  fileId: number | undefined,
  libraryId: number | undefined,
  roomId: string | undefined,
  roomToken: string | undefined,
  restart: boolean,
  audioTrackIndex: number | undefined,
  prePlaySubtitleMode: "auto" | "off" | "explicit" | undefined,
  prePlaySubtitleSelection: PrePlaySubtitleSelection | null | undefined,
): string {
  return JSON.stringify([
    contentId,
    fileId ?? null,
    libraryId ?? null,
    roomId ?? null,
    roomToken ?? null,
    restart,
    audioTrackIndex ?? null,
    prePlaySubtitleMode ?? null,
    prePlaySubtitleSelection ?? null,
  ]);
}

export function createWatchRouteRequest({
  contentId,
  fileId,
  libraryId,
  roomId,
  roomToken,
  restart = false,
  audioTrackIndex,
  prePlaySubtitleMode,
  prePlaySubtitleSelection,
  returnHref,
}: WatchPlaybackStartInput): WatchRouteRequest {
  return {
    contentId,
    fileId,
    libraryId,
    roomId,
    roomToken,
    restart,
    audioTrackIndex,
    prePlaySubtitleMode,
    prePlaySubtitleSelection,
    returnHref,
    requestKey: buildWatchRouteRequestKey(
      contentId,
      fileId,
      libraryId,
      roomId,
      roomToken,
      restart,
      audioTrackIndex,
      prePlaySubtitleMode,
      prePlaySubtitleSelection,
    ),
  };
}

export function buildWatchRouteRequest(
  contentId: string,
  searchParams: URLSearchParams,
): WatchRouteRequest {
  return createWatchRouteRequest({
    contentId,
    fileId: parseOptionalInt(searchParams.get("fileId")),
    libraryId: parseOptionalInt(searchParams.get("libraryId")),
    roomId: searchParams.get("room_id") ?? undefined,
    roomToken: searchParams.get("room_token") ?? undefined,
    restart: searchParams.get("restart") === "1",
  });
}

export function buildWatchItemHref(request: WatchRouteRequest): string {
  return `/item/${request.contentId}${request.libraryId ? `?libraryId=${request.libraryId}` : ""}`;
}

export function buildWatchHref(request: WatchRouteRequest): string {
  const searchParams = new URLSearchParams();
  if (request.fileId != null) searchParams.set("fileId", String(request.fileId));
  if (request.libraryId != null) searchParams.set("libraryId", String(request.libraryId));
  if (request.roomId) searchParams.set("room_id", request.roomId);
  if (request.roomToken) searchParams.set("room_token", request.roomToken);
  if (request.restart) searchParams.set("restart", "1");
  const query = searchParams.toString();

  return `/watch/${request.contentId}${query ? `?${query}` : ""}`;
}

export function parseWatchHref(href: string): WatchRouteRequest | null {
  try {
    const url = new URL(href, "http://localhost");
    const match = url.pathname.match(/^\/watch\/([^/]+)$/);
    if (!match) {
      return null;
    }

    return buildWatchRouteRequest(decodeURIComponent(match[1] ?? ""), url.searchParams);
  } catch {
    return null;
  }
}

type DerivedWatchPageProps = Omit<
  WatchPageProps,
  | "playbackRequestKey"
  | "onExit"
  | "onNavigateEpisode"
  | "displayMode"
  | "onPictureInPictureChange"
  | "autoEnterPictureInPicture"
  | "onPlaybackStateChange"
  | "onPlaybackTransportReady"
>;

/**
 * The pre-cutover cap, used when no canonical value was passed. The settings
 * screen no longer writes this column, so it only carries a choice made before
 * the contract landed.
 */
function currentProfileQualityFallback(profile?: Profile | null): string | null {
  return profile?.quality_preference || null;
}

function buildInitialSubtitleTrackIndexes({
  item,
  audioTrackIndex,
  subtitleMode,
  preferredSubtitleLanguage,
  preferredSubtitleTrackSignature,
  profileLanguage,
  showForcedSubtitles,
}: {
  item: WatchDetail;
  audioTrackIndex?: number;
  subtitleMode: SubtitleMode;
  preferredSubtitleLanguage: string | null;
  preferredSubtitleTrackSignature: PlayerSubtitleTrackSignature | null;
  profileLanguage: string | null;
  showForcedSubtitles: boolean;
}): {
  start: Record<number, number>;
  bitmap: Record<number, number>;
} {
  const start: Record<number, number> = {};
  const bitmap: Record<number, number> = {};
  // Downloaded tracks are session inventory, not part of watch detail, so the
  // client cannot know their combined ordinal before the server creates one.
  if (preferredSubtitleTrackSignature?.source === "downloaded") {
    return { start, bitmap };
  }

  for (const version of item.versions) {
    // Playback v3 assigns dense combined ordinals with sidecars first and
    // embedded tracks second. Catalog stream indexes are not those ordinals.
    const orderedTracks = [
      ...(version.subtitle_tracks ?? []).filter((track) => track.external),
      ...(version.subtitle_tracks ?? []).filter((track) => !track.external),
    ];
    const tracks: PlayerSubtitleInfo[] = orderedTracks.map((track, index) => ({
      index,
      language: track.language?.trim() || "unknown",
      codec: track.codec,
      label:
        track.title?.trim() ||
        track.embedded_title?.trim() ||
        track.file_name?.trim() ||
        track.language?.trim() ||
        `Subtitle ${index + 1}`,
      source: track.external ? "external" : "embedded",
      forced: track.forced,
      hearing_impaired: track.hearing_impaired,
      url: "",
    }));
    const selectedAudioTrackIndex =
      audioTrackIndex ??
      version.effective_audio_track_index ??
      (version.audio_tracks?.length ? 0 : null);
    const selectedSubtitleTrackIndex = resolveSubtitleAutoSelect({
      mode: subtitleMode,
      tracks,
      preferredLanguage: preferredSubtitleLanguage,
      preferredTrackSignature: preferredSubtitleTrackSignature,
      audioLanguage: resolveVersionAudioLanguage(version, selectedAudioTrackIndex),
      profileLanguage,
      showForcedSubtitles,
    });
    if (selectedSubtitleTrackIndex === null) {
      continue;
    }
    const selectedTrack = tracks.find((track) => track.index === selectedSubtitleTrackIndex);
    start[version.file_id] = selectedSubtitleTrackIndex;
    // Bitmap tracks (PGS/DVD/DVB) have to be burned in on the web player. Keep
    // their ordinals separately so a refused start can be retried without the
    // subtitle. Successful starts stay a single request, and the server owns
    // remapping the selection when it adapts to another file version.
    if (selectedTrack && isBitmapCodec(selectedTrack.codec)) {
      bitmap[version.file_id] = selectedSubtitleTrackIndex;
    }
  }

  return { start, bitmap };
}

export function buildWatchPageProps({
  request,
  item,
  currentProfile,
  seriesEpisodes,
  qualityPreference = currentProfileQualityFallback(currentProfile),
}: {
  request: WatchRouteRequest;
  item: WatchDetail;
  currentProfile?: Profile | null;
  seriesEpisodes?: EpisodeRef[];
  /**
   * The resolution cap from the settings contract, via useQualityPreference.
   * Passed in rather than read here because this is a pure builder: the
   * canonical value is where the quality picker writes, and the profile column
   * it falls back to is no longer updated by that picker.
   */
  qualityPreference?: string | null;
}): DerivedWatchPageProps {
  const basePreferredSubtitleLanguage =
    item.effective_subtitle_language !== undefined
      ? item.effective_subtitle_language
      : currentProfile?.subtitle_language || null;
  const baseSubtitleMode = (item.effective_subtitle_mode ??
    currentProfile?.subtitle_mode ??
    "auto") as SubtitleMode;
  const showForcedSubtitles =
    item.effective_show_forced_subtitles ?? currentProfile?.show_forced_subtitles ?? true;
  const profileLanguage = currentProfile?.language || null;

  const subtitles: PlayerSubtitleInfo[] = item.subtitles.map((subtitle, index) => ({
    index,
    language: subtitle.language,
    codec: subtitle.codec,
    label: subtitle.title || subtitle.language,
    source: subtitle.source === "external" ? "external" : "embedded",
    forced: subtitle.forced,
    hearing_impaired: subtitle.hearing_impaired,
    url: "",
  }));
  const preferredSubtitleTrackSignature: PlayerSubtitleTrackSignature | null =
    item.effective_subtitle_track_signature
      ? {
          source:
            item.effective_subtitle_track_signature.source === "downloaded"
              ? "downloaded"
              : item.effective_subtitle_track_signature.source === "external"
                ? "external"
                : item.effective_subtitle_track_signature.source === "embedded"
                  ? "embedded"
                  : undefined,
          language: item.effective_subtitle_track_signature.language,
          codec: item.effective_subtitle_track_signature.codec,
          label: item.effective_subtitle_track_signature.label,
          forced: item.effective_subtitle_track_signature.forced,
          hearing_impaired: item.effective_subtitle_track_signature.hearing_impaired,
        }
      : null;
  const requestSubtitleSelection = request.prePlaySubtitleSelection;
  const preferredSubtitleLanguage =
    request.prePlaySubtitleMode === "off"
      ? ""
      : request.prePlaySubtitleMode === "explicit"
        ? (requestSubtitleSelection?.language ?? null)
        : basePreferredSubtitleLanguage;
  const subtitleMode =
    request.prePlaySubtitleMode === "off"
      ? "off"
      : request.prePlaySubtitleMode === "explicit"
        ? "always"
        : baseSubtitleMode;
  const effectivePreferredSubtitleTrackSignature =
    request.prePlaySubtitleMode === "explicit" && requestSubtitleSelection
      ? ({
          source: requestSubtitleSelection?.source,
          language: requestSubtitleSelection?.language,
          codec: requestSubtitleSelection?.codec,
          label: requestSubtitleSelection?.label,
          forced: requestSubtitleSelection?.forced,
          hearing_impaired: requestSubtitleSelection?.hearing_impaired,
        } satisfies PlayerSubtitleTrackSignature)
      : request.prePlaySubtitleMode === "off"
        ? null
        : preferredSubtitleTrackSignature;
  const initialSubtitleTrackIndexes = buildInitialSubtitleTrackIndexes({
    item,
    audioTrackIndex: request.audioTrackIndex,
    subtitleMode,
    preferredSubtitleLanguage,
    preferredSubtitleTrackSignature: effectivePreferredSubtitleTrackSignature,
    profileLanguage,
    showForcedSubtitles,
  });

  const intro: PlayerTimeRange | null = item.intro ?? null;
  const credits: PlayerTimeRange | null = item.credits ?? null;
  const recap: PlayerTimeRange | null = item.recap ?? null;
  const preview: PlayerTimeRange | null = item.preview ?? null;
  // The profile DTO is the compatibility fallback for servers before settings
  // contract revision 7. WatchPlaybackHost replaces this with the canonical
  // enum whenever the connected server advertises it.
  const introSkipMode: IntroSkipMode = currentProfile?.auto_skip_intro ? "always" : "ask";
  const autoSkipRecap = currentProfile?.auto_skip_recap ?? false;
  const autoPlayNextPreview = currentProfile?.auto_play_next_preview ?? false;
  // Watched items store position 0, so any nonzero position is a live resume
  // point — including a rewatch in flight (played stays true).
  const initialPosition = request.restart ? 0 : (item.user_data?.position_seconds ?? 0);

  const resumeHints: ResumeHints | undefined =
    item.user_data?.last_file_id != null ||
    item.user_data?.last_resolution != null ||
    item.user_data?.last_hdr != null ||
    item.user_data?.last_codec_video != null ||
    item.user_data?.last_edition_key != null
      ? {
          lastFileId: item.user_data.last_file_id,
          lastResolution: item.user_data.last_resolution,
          lastHDR: item.user_data.last_hdr,
          lastCodecVideo: item.user_data.last_codec_video,
          lastEditionKey: item.user_data.last_edition_key,
        }
      : item.effective_version_resolution !== undefined ||
          item.effective_version_hdr !== undefined ||
          item.effective_version_codec_video !== undefined ||
          item.effective_version_edition_key !== undefined
        ? {
            lastResolution: item.effective_version_resolution,
            lastHDR: item.effective_version_hdr,
            lastCodecVideo: item.effective_version_codec_video,
            lastEditionKey: item.effective_version_edition_key,
          }
        : undefined;

  return {
    contentId: request.contentId,
    title: item.title,
    year: item.year,
    fileId: request.fileId,
    libraryId: request.libraryId,
    versions: item.versions,
    playbackVariants: item.playback_variants ?? [],
    subtitles,
    initialPosition,
    forceInitialPosition: request.restart,
    qualityPreference,
    explicitAudioTrackIndex: request.audioTrackIndex ?? null,
    initialSubtitleTrackIndexByFileId: initialSubtitleTrackIndexes.start,
    initialBitmapSubtitleTrackIndexByFileId: initialSubtitleTrackIndexes.bitmap,
    preferredSubtitleLanguage,
    preferredSubtitleTrackSignature: effectivePreferredSubtitleTrackSignature,
    subtitleMode,
    showForcedSubtitles,
    profileLanguage,
    intro,
    introSkipMode,
    credits,
    recap,
    preview,
    autoSkipRecap,
    autoPlayNextPreview,
    seriesContext: item.series_id
      ? {
          seriesId: item.series_id,
          seriesTitle: item.series_title,
          currentSeason: item.season_number ?? 0,
          currentEpisode: item.episode_number ?? 0,
          episodes: seriesEpisodes ?? [],
        }
      : undefined,
    resumeHints,
    watchTogetherRoomId: request.roomId ?? null,
    watchTogetherRoomToken: request.roomToken ?? null,
  };
}
