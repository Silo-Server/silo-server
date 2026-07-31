/**
 * Self-contained types for the player module.
 * Zero imports from app-specific code.
 */

/** Subtitle display mode. */
export type SubtitleMode = "off" | "auto" | "always";

/** A file version available for playback. */
export interface PlayerFileVersion {
  file_id: number;
  file_name?: string;
  resolution: string;
  codec_video: string;
  codec_audio: string;
  hdr: boolean;
  container: string;
  file_size: number;
  duration: number;
  bitrate: number;
  edition_key?: string;
  presentation_kind?: string;
  presentation_group_key?: string;
  presentation_part_index?: number;
  effective_audio_track_index?: number;
  effective_audio_language?: string;
  audio_channels?: number;
  video_tracks?: PlayerVideoTrack[];
  audio_tracks?: PlayerAudioTrack[];
  chapters?: PlayerChapter[];
  intro?: PlayerTimeRange | null;
  credits?: PlayerTimeRange | null;
  recap?: PlayerTimeRange | null;
  preview?: PlayerTimeRange | null;
}

export interface PlayerPlaybackVariantPart {
  part_index: number;
  default_file_id?: number;
  total_duration?: number;
  versions: PlayerFileVersion[];
}

export interface PlayerPlaybackVariant {
  variant_id: string;
  edition_raw?: string;
  edition_key?: string;
  presentation_kind?: string;
  presentation_group_key?: string;
  part_count: number;
  total_duration?: number;
  default_file_id?: number;
  parts: PlayerPlaybackVariantPart[];
}

export interface PlayerChapter {
  index: number;
  title: string;
  start_seconds: number;
  end_seconds: number;
  source: string;
  thumbnail_url?: string;
  thumbnail_thumbhash?: string;
}

export interface PlayerVideoTrack {
  title?: string;
  codec?: string;
  dolby_vision?: string;
  profile?: string;
  level?: number;
  width?: number;
  height?: number;
  aspect_ratio?: string;
  interlaced?: boolean;
  frame_rate?: string;
  bitrate?: number;
  video_range?: string;
  color_range?: string;
  color_primaries?: string;
  color_space?: string;
  color_transfer?: string;
  bit_depth?: number;
  pixel_format?: string;
  reference_frames?: number;
}

export interface PlayerAudioTrack {
  title?: string;
  embedded_title?: string;
  language?: string;
  codec?: string;
  layout?: string;
  channels?: number;
  bitrate?: number;
  sample_rate?: number;
  bit_depth?: number;
  default?: boolean;
}

/** Subtitle track information. */
export interface PlayerSubtitleInfo {
  /**
   * The server's combined ordinal for this track, copied verbatim from the
   * plan's subtitle inventory. It is the identity echoed back on a track
   * change; it is never derived by counting or summing track arrays.
   */
  index: number;
  /** Media file whose subtitle inventory assigned this track index. */
  media_file_id?: number;
  /**
   * The server publishes this track but cannot deliver it as a sidecar, so
   * selecting it asks the server to composite it into the video instead of
   * rendering it in the browser.
   */
  burn_in_only?: boolean;
  /** Server-assigned track identity, echoed back on a track change. */
  track_id?: string;
  language: string;
  codec?: string;
  label: string;
  source?: "external" | "embedded" | "downloaded";
  forced?: boolean;
  hearing_impaired?: boolean;
  url: string;
  /**
   * Optional endpoint returning base64-encoded container font attachments for
   * ASS/SSA rendering. Present for embedded ASS tracks when the backend can
   * extract attachments from the source media file.
   */
  font_bundle_url?: string;
  /**
   * When true, this is an in-progress AI translation whose cues arrive over the
   * realtime websocket rather than from `url`. `useSubtitleTracks` feeds it from
   * the `liveCues` source instead of fetching.
   */
  live?: boolean;
}

export interface PlayerSubtitleTrackSignature {
  source?: "external" | "embedded" | "downloaded";
  language?: string;
  codec?: string;
  label?: string;
  forced?: boolean;
  hearing_impaired?: boolean;
}

export interface PrePlaySubtitleSelection {
  source: "embedded" | "external" | "downloaded";
  language?: string;
  codec?: string;
  label?: string;
  forced?: boolean;
  hearing_impaired?: boolean;
  external_subtitle_path?: string;
  downloaded_subtitle_id?: number;
  /** Backend track index, carried so the selection can persist as a preference. */
  track_index?: number;
}

/** A time range (intro start/end or credits start/end). */
export interface PlayerTimeRange {
  start: number;
  end: number;
}

/**
 * The four editable marker/segment kinds. Note "credits" is what the
 * Jellyfin-compatible API exposes as "Outro" — there is no separate outro kind.
 */
export type MarkerKind = "intro" | "recap" | "credits" | "preview";

/** A full set of editable marker ranges for one file (null = no marker). */
export interface MarkerDraft {
  intro: PlayerTimeRange | null;
  recap: PlayerTimeRange | null;
  credits: PlayerTimeRange | null;
  preview: PlayerTimeRange | null;
}

/** A marker range positioned on the seek bar, tagged with its kind for color. */
export interface MarkerRegionView {
  kind: MarkerKind;
  start: number;
  end: number;
}

/** Hints from the last playback session for version selection on resume. */
export interface ResumeHints {
  lastFileId?: number;
  lastResolution?: string;
  lastHDR?: boolean;
  lastCodecVideo?: string;
  lastEditionKey?: string;
}

/** Local playback snapshot captured when the user exits the player. */
export interface PlaybackExitState {
  positionSeconds: number;
  durationSeconds?: number;
  lastFileId?: number | null;
  lastResolution?: string;
  lastHDR?: boolean;
  lastCodecVideo?: string;
  lastEditionKey?: string;
  destinationHref?: string;
}

export type PlayerDisplayMode = "foreground" | "detached" | "postroll";

export interface PlayerPictureInPictureChange {
  active: boolean;
  playbackContinues: boolean;
}

export interface PlayerPlaybackStateChange {
  currentTime: number;
  duration: number;
  playing: boolean;
}

export interface PlayerPlaybackTransport {
  playPause: () => void | Promise<void>;
  seekBy: (secondsDelta: number) => void;
  seekTo: (seconds: number) => void;
  togglePictureInPicture: () => void | Promise<void>;
}

/** Props for the top-level WatchPage component. */
export interface WatchPageProps {
  contentId: string;
  title: string;
  year?: number;
  fileId?: number;
  libraryId?: number;
  versions: PlayerFileVersion[];
  playbackVariants?: PlayerPlaybackVariant[];
  subtitles: PlayerSubtitleInfo[];
  initialPosition?: number;
  forceInitialPosition?: boolean;
  qualityPreference?: string | null;
  /** Bandwidth cap in kbps from playback.max_bitrate_kbps; null/undefined is uncapped. */
  maxBitrateKbps?: number | null;
  explicitAudioTrackIndex?: number | null;
  preferredSubtitleLanguage?: string | null;
  preferredSubtitleTrackSignature?: PlayerSubtitleTrackSignature | null;
  subtitleMode?: SubtitleMode;
  showForcedSubtitles?: boolean;
  profileLanguage?: string | null;
  intro: PlayerTimeRange | null;
  autoSkipIntro?: boolean;
  credits: PlayerTimeRange | null;
  recap?: PlayerTimeRange | null;
  preview?: PlayerTimeRange | null;
  autoSkipRecap?: boolean;
  autoPlayNextPreview?: boolean;
  canEditMarkers?: boolean;
  seriesContext?: SeriesContext;
  onNavigateEpisode?: (contentId: string) => void;
  onEnded?: (state?: PlaybackExitState) => void | Promise<void>;
  onExit: (state?: PlaybackExitState) => void | Promise<void>;
  onMinimize?: (state?: PlaybackExitState) => void | Promise<void>;
  resumeHints?: ResumeHints;
  playbackRequestKey?: string;
  watchTogetherRoomId?: string | null;
  watchTogetherRoomToken?: string | null;
  displayMode?: PlayerDisplayMode;
  onPictureInPictureChange?: (change: PlayerPictureInPictureChange) => void;
  autoEnterPictureInPicture?: boolean;
  onPlaybackStateChange?: (state: PlayerPlaybackStateChange) => void;
  onPlaybackTransportReady?: (transport: PlayerPlaybackTransport | null) => void;
  onReturnFromPostRoll?: () => void;
}

/** A quality option shown in the player settings menu. */
export interface QualityOption {
  id: string;
  label: string;
  sublabel: string;
  resolution: string;
  bitrateKbps: number;
  isOriginal: boolean;
}

/** Context for series playback (episode navigation). */
export interface SeriesContext {
  seriesId: string;
  seriesTitle?: string;
  currentSeason: number;
  currentEpisode: number;
  episodes: EpisodeRef[];
}

/** Minimal episode reference for the player. */
export interface EpisodeRef {
  contentId: string;
  seasonNumber: number;
  episodeNumber: number;
  title: string;
  runtime: number;
  /** Optional display fields for post-roll / next-episode UI. */
  overview?: string;
  stillUrl?: string;
  stillThumbhash?: string;
  airDate?: string | null;
}
