// Public API for the player module.
// This module has ZERO imports from app-specific code (@/api/, @/hooks/, @/components/).

export { PlayerConfigProvider } from "./context/PlayerConfigContext";
export type { PlayerConfig } from "./context/PlayerConfigContext";
export { WatchPage } from "./components/WatchPage";
export type {
  WatchPageProps,
  PlayerChapter,
  PlayerFileVersion,
  PlayerSubtitleInfo,
  PlayerSubtitleTrackSignature,
  PrePlaySubtitleSelection,
  PlayerTimeRange,
  PlaybackExitState,
  PlayerDisplayMode,
  PlayerPictureInPictureChange,
  PlayerPlaybackStateChange,
  PlayerPlaybackTransport,
  SeriesContext,
  EpisodeRef,
  IntroSkipMode,
  SubtitleMode,
  ResumeHints,
} from "./types";
