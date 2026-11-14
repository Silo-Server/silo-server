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
import type { PlayerPlaybackVariant } from "@/player/types";
import { resolveVersionAudioLanguage } from "@/player/utils/effectiveAudioLanguage";
import { subtitleNeedsBurnIn } from "@/player/utils/subtitleCodecs";
import { buildPublishedSubtitleTracks } from "@/player/utils/subtitleInventory";
import { resolveSubtitleAutoSelect } from "@/player/utils/subtitleSort";

export interface WatchRouteRequest {
  contentId: string;
  fileId?: number;
  libraryId?: number;
  roomId?: string;
  roomToken?: string;
  /** The shuffle this playback belongs to; the post-roll plays its next pick. */
  shuffleId?: string;
  restart: boolean;
  audioTrackIndex?: number;
  prePlaySubtitleMode?: "auto" | "off" | "explicit";
  prePlaySubtitleSelection?: PrePlaySubtitleSelection | null;
  /** The viewer chose fileId, so Auto must not swap in another version. */
  versionPicked?: boolean;
  returnHref?: string;
  requestKey: string;
  /** True when `fileId` was explicitly chosen by the viewer on the media page. */
  explicitFileSelection?: boolean;
  /** When true, the server should force a re-link/re-query of the virtual file
   *  on this start attempt. Only set when the viewer explicitly picks an
   *  unavailable version. */
  forceRelink?: boolean;
}

export interface WatchPlaybackStartInput {
  contentId: string;
  fileId?: number;
  libraryId?: number;
  roomId?: string;
  roomToken?: string;
  shuffleId?: string;
  restart?: boolean;
  audioTrackIndex?: number;
  prePlaySubtitleMode?: "auto" | "off" | "explicit";
  prePlaySubtitleSelection?: PrePlaySubtitleSelection | null;
  versionPicked?: boolean;
  returnHref?: string;
  explicitFileSelection?: boolean;
  /** When true, the server should force a re-link/re-query of the virtual file
   *  on this start attempt. Only set when the viewer explicitly picks an
   *  unavailable version. */
  forceRelink?: boolean;
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
  shuffleId: string | undefined,
  restart: boolean,
  audioTrackIndex: number | undefined,
  prePlaySubtitleMode: "auto" | "off" | "explicit" | undefined,
  prePlaySubtitleSelection: PrePlaySubtitleSelection | null | undefined,
  versionPicked: boolean,
  explicitFileSelection: boolean,
): string {
  return JSON.stringify([
    contentId,
    fileId ?? null,
    libraryId ?? null,
    roomId ?? null,
    roomToken ?? null,
    shuffleId ?? null,
    restart,
    audioTrackIndex ?? null,
    prePlaySubtitleMode ?? null,
    prePlaySubtitleSelection ?? null,
    versionPicked,
    // Two starts that differ only here are different requests: one lets the
    // server pick the file, the other pins it. A route sync must not fold the
    // second into the first.
    explicitFileSelection,
  ]);
}

export function createWatchRouteRequest({
  contentId,
  fileId,
  libraryId,
  roomId,
  roomToken,
  shuffleId,
  restart = false,
  audioTrackIndex,
  prePlaySubtitleMode,
  prePlaySubtitleSelection,
  versionPicked = false,
  returnHref,
  explicitFileSelection,
  forceRelink,
}: WatchPlaybackStartInput): WatchRouteRequest {
  return {
    contentId,
    fileId,
    libraryId,
    roomId,
    roomToken,
    shuffleId,
    restart,
    audioTrackIndex,
    prePlaySubtitleMode,
    prePlaySubtitleSelection,
    versionPicked,
    returnHref,
    explicitFileSelection,
    forceRelink,
    requestKey: buildWatchRouteRequestKey(
      contentId,
      fileId,
      libraryId,
      roomId,
      roomToken,
      shuffleId,
      restart,
      audioTrackIndex,
      prePlaySubtitleMode,
      prePlaySubtitleSelection,
      versionPicked,
      explicitFileSelection === true,
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
    shuffleId: searchParams.get("shuffle") ?? undefined,
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
  if (request.shuffleId) searchParams.set("shuffle", request.shuffleId);
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
  | "seekIntervals"
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
  showForcedSubtitles,
}: {
  item: WatchDetail;
  audioTrackIndex?: number;
  subtitleMode: SubtitleMode;
  preferredSubtitleLanguage: string | null;
  preferredSubtitleTrackSignature: PlayerSubtitleTrackSignature | null;
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
    // The helper mirrors the server's BuildSubtitleInventoryV3 so `index` is the
    // published dense combined ordinal, not the track's position in the wire
    // array. Downloadless versions still resolve because the helper assigns the
    // same ordinals the server will.
    const tracks = buildPublishedSubtitleTracks(version.subtitle_tracks);
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
      showForcedSubtitles,
    });
    if (selectedSubtitleTrackIndex === null) {
      continue;
    }
    const selectedTrack = tracks.find((track) => track.index === selectedSubtitleTrackIndex);
    start[version.file_id] = selectedSubtitleTrackIndex;
    // Tracks the server must burn in (DVD/DVB bitmaps, external bitmap
    // files) can be why a start is refused. Keep their ordinals separately so
    // a refused start can be retried without the subtitle. Successful starts
    // stay a single request, and the server owns remapping the selection when
    // it adapts to another file version.
    if (selectedTrack && subtitleNeedsBurnIn(selectedTrack)) {
      bitmap[version.file_id] = selectedSubtitleTrackIndex;
    }
  }

  return { start, bitmap };
}

// Stable empty reference: the array identity reaches the player's session hook
// and its legacy catalog poll, so a fresh `[]` on every render would restart the
// poll's attempt budget and clear its terminal failure badge.
const EMPTY_PLAYBACK_VARIANTS: PlayerPlaybackVariant[] = [];

export function buildWatchPageProps({
  request,
  item,
  currentProfile,
  seriesEpisodes,
  qualityPreference = currentProfileQualityFallback(currentProfile),
  requestedFileId = null,
  requestedVersionExplicit = false,
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
  /**
   * The row the viewer last requested for this item, from the client's
   * durable selection store. The server's `last_file_id` names the row that
   * actually served the bytes, which can be a substitute; resuming must ask
   * for the viewer's own pick. Passed in for the same purity reason as the
   * quality cap.
   */
  requestedFileId?: number | null;
  /**
   * Whether that remembered row is one the viewer named themselves. A reload
   * keeps the row but loses the navigation that carried `explicitFileSelection`,
   * so the store's standing is the only thing that can still tell a pick from
   * an Auto start.
   */
  requestedVersionExplicit?: boolean;
}): DerivedWatchPageProps {
  const basePreferredSubtitleLanguage =
    item.effective_subtitle_language !== undefined
      ? item.effective_subtitle_language
      : currentProfile?.subtitle_language || null;
  const baseSubtitleMode = (item.effective_subtitle_mode ||
    currentProfile?.subtitle_mode ||
    "off") as SubtitleMode;
  const showForcedSubtitles =
    item.effective_show_forced_subtitles ?? currentProfile?.show_forced_subtitles ?? true;

  const requestedVersion =
    (request.fileId ? item.versions.find((v) => v.file_id === request.fileId) : undefined) ??
    item.versions[0];
  // One shared derivation for every ordinal the client computes; see
  // subtitleInventory.ts. Watch detail carries the file's own tracks, so the
  // published ordinals match the server's for the same inventory. An older
  // detail that omits `subtitle_tracks` only has the item's subtitle rows;
  // they go through the same rule so their menu ordinals stay dense and
  // gap-free instead of trusting the array position.
  const subtitles: PlayerSubtitleInfo[] =
    requestedVersion?.subtitle_tracks !== undefined
      ? buildPublishedSubtitleTracks(requestedVersion.subtitle_tracks)
      : buildPublishedSubtitleTracks(
          item.subtitles.map((subtitle) => ({
            language: subtitle.language,
            codec: subtitle.codec,
            title: subtitle.title || subtitle.language,
            forced: subtitle.forced,
            hearing_impaired: subtitle.hearing_impaired,
            external: subtitle.source === "external",
          })),
        );
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
    showForcedSubtitles,
  });
  // A pre-play pick names its row by the dense combined ordinal the popover
  // rendered, not just by descriptors. Resolving it through the signature again
  // would hand playback the first track whose language and codec match, so two
  // same-language streams (two German SRT tracks in one release) both start the
  // first one. The viewer's ordinal is authoritative for the version they chose;
  // other versions still fall back to the signature for server-side adaptation.
  const explicitRequestedTrackIndex =
    request.prePlaySubtitleMode === "explicit" &&
    requestSubtitleSelection != null &&
    requestSubtitleSelection.source !== "downloaded" &&
    requestSubtitleSelection.track_index != null &&
    requestedVersion != null &&
    subtitles.some((track) => track.index === requestSubtitleSelection.track_index)
      ? requestSubtitleSelection.track_index
      : null;
  if (explicitRequestedTrackIndex !== null && requestedVersion) {
    const fileId = requestedVersion.file_id;
    initialSubtitleTrackIndexes.start[fileId] = explicitRequestedTrackIndex;
    const requestedTrack =
      subtitles.find((track) => track.index === explicitRequestedTrackIndex) ?? null;
    if (requestedTrack && subtitleNeedsBurnIn(requestedTrack)) {
      initialSubtitleTrackIndexes.bitmap[fileId] = explicitRequestedTrackIndex;
    } else {
      delete initialSubtitleTrackIndexes.bitmap[fileId];
    }
  }

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

  // Recognition of the release to ask for: the viewer's requested row when the
  // client remembers one, else the row the server says actually played. A
  // substitution must not replace the release they picked.
  const userData = item.user_data;
  const resumeFileId = requestedFileId ?? userData?.last_file_id;
  const resumeHints: ResumeHints | undefined =
    resumeFileId != null ||
    userData?.last_resolution != null ||
    userData?.last_hdr != null ||
    userData?.last_codec_video != null ||
    userData?.last_edition_key != null
      ? {
          lastFileId: resumeFileId ?? undefined,
          lastResolution: userData?.last_resolution,
          lastHDR: userData?.last_hdr,
          lastCodecVideo: userData?.last_codec_video,
          lastEditionKey: userData?.last_edition_key,
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
    versionPicked: request.versionPicked,
    libraryId: request.libraryId,
    versions: item.versions,
    playbackVariants: item.playback_variants ?? EMPTY_PLAYBACK_VARIANTS,
    // Omitted (rather than a fresh `[]`) when the detail carries none: a new
    // array identity on every render would loop the player's sync effect.
    ...(item.indexer_releases?.length ? { indexerReleases: item.indexer_releases } : {}),
    virtualRanking: item.virtual_ranking,
    subtitles,
    initialPosition,
    forceInitialPosition: request.restart,
    qualityPreference,
    explicitAudioTrackIndex: request.audioTrackIndex ?? null,
    // The navigation that carried the pick says so directly. A reload or a
    // directly opened watch URL has only the store's standing to go on, and it
    // counts only while it still names the row this request asks for: a row
    // the viewer merely let the server choose must stay substitutable.
    explicitFileSelection:
      request.explicitFileSelection === true ||
      (request.fileId != null && request.fileId === requestedFileId && requestedVersionExplicit),
    forceRelink: request.forceRelink,
    initialSubtitleTrackIndexByFileId: initialSubtitleTrackIndexes.start,
    initialBitmapSubtitleTrackIndexByFileId: initialSubtitleTrackIndexes.bitmap,
    preferredSubtitleLanguage,
    preferredSubtitleTrackSignature: effectivePreferredSubtitleTrackSignature,
    subtitleMode,
    showForcedSubtitles,
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
