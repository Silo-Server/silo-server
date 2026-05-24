import type { Profile, WatchDetail } from "@/api/types";
import type {
  EpisodeRef,
  PrePlaySubtitleSelection,
  PlayerSubtitleInfo,
  PlayerSubtitleTrackSignature,
  PlayerTimeRange,
  ResumeHints,
  SubtitleMode,
  WatchPageProps,
} from "@/player";

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

export function buildWatchPageProps({
  request,
  item,
  currentProfile,
  seriesEpisodes,
}: {
  request: WatchRouteRequest;
  item: WatchDetail;
  currentProfile?: Profile | null;
  seriesEpisodes?: EpisodeRef[];
}): DerivedWatchPageProps {
  const qualityPreference = currentProfile?.quality_preference || null;
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

  const intro: PlayerTimeRange | null = item.intro ?? null;
  const credits: PlayerTimeRange | null = item.credits ?? null;
  const recap: PlayerTimeRange | null = item.recap ?? null;
  const preview: PlayerTimeRange | null = item.preview ?? null;
  const autoSkipIntro = currentProfile?.auto_skip_intro ?? false;
  const autoSkipRecap = currentProfile?.auto_skip_recap ?? false;
  const autoPlayNextPreview = currentProfile?.auto_play_next_preview ?? false;
  const initialPosition = request.restart
    ? 0
    : item.user_data?.played === true
      ? 0
      : (item.user_data?.position_seconds ?? 0);

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
    preferredSubtitleLanguage,
    preferredSubtitleTrackSignature: effectivePreferredSubtitleTrackSignature,
    subtitleMode,
    showForcedSubtitles,
    profileLanguage,
    intro,
    autoSkipIntro,
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
