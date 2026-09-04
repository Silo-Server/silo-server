import { useState, type ReactNode } from "react";
import {
  Info,
  ListVideo,
  Maximize,
  Minimize,
  MoreHorizontal,
  Pause,
  PictureInPicture2,
  Play,
  RotateCcw,
  RotateCw,
  SkipBack,
  SkipForward,
  Tags,
  AudioLines,
} from "lucide-react";
import { CircleButton } from "./CircleButton";
import { SeekBar, formatTime } from "./SeekBar";
import { VolumeControl } from "./VolumeControl";
import { QualityMenu } from "./QualityMenu";
import { SubtitleMenu } from "./SubtitleMenu";
import { AudioTrackMenu } from "./AudioTrackMenu";
import { ChaptersMenu } from "./ChaptersMenu";
import type {
  MarkerKind,
  MarkerRegionView,
  PlayerAudioTrack,
  PlayerChapter,
  PlayerSubtitleInfo,
  QualityOption,
} from "../types";
import type { VersionInfo } from "./QualityMenu";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import { useCoarsePointer } from "../hooks/useCoarsePointer";
import { PlayerMenuSurface } from "./PlayerMenuSurface";

interface PlayerControlsProps {
  // Visibility
  visible: boolean;
  // Video state
  playing: boolean;
  currentTime: number;
  duration: number;
  buffered: TimeRanges | null;
  // Seek bar markers
  chapters?: PlayerChapter[];
  regions?: MarkerRegionView[];
  // Marker editing
  editing?: boolean;
  activeEditKind?: MarkerKind | null;
  onRegionEdgeChange?: (kind: MarkerKind, edge: "start" | "end", seconds: number) => void;
  markerEditAvailable?: boolean;
  markerEditActive?: boolean;
  onToggleMarkerEdit?: () => void;
  volume: number;
  muted: boolean;
  isFullscreen: boolean;
  // Subtitles
  subtitleTracks: PlayerSubtitleInfo[];
  activeSubtitleIndex: number | null;
  onSubtitleSelect: (index: number | null) => void;
  subtitleDelayMs: number;
  onSubtitleDelayChange: (ms: number) => void;
  preferredSubtitleLanguage?: string | null;
  mediaFileId?: number;
  playerConfig?: PlayerConfig;
  onRefreshSubtitles?: () => void;
  sessionId?: string;
  getSubtitleStartPosition?: () => number;
  // Audio
  audioTracks: PlayerAudioTrack[];
  activeAudioIndex: number;
  onAudioSelect?: (index: number, currentPosition: number) => void;
  // Quality
  qualityOptions: QualityOption[];
  activeQualityId: string;
  isTranscoding: boolean;
  qualityError: string | null;
  onQualitySelect: (id: string) => void;
  // Version switching
  versions?: VersionInfo[];
  onSwitchVersion?: (fileId: number) => void;
  // PiP
  onTogglePiP?: () => void;
  // Playback info
  showPlaybackInfo: boolean;
  onTogglePlaybackInfo: () => void;
  // Episode navigation
  hasPrevEpisode?: boolean;
  hasNextEpisode?: boolean;
  onPrevEpisode?: () => void;
  onNextEpisode?: () => void;
  // Title strip
  title?: string;
  subtitleLabel?: string;
  // Callbacks
  onPlayPause: () => void;
  onSeek: (seconds: number) => void;
  onVolumeChange: (volume: number) => void;
  onMutedChange: (muted: boolean) => void;
  onFullscreenToggle: () => void;
  onSurfaceTap?: () => void;
}

/** Skip amount for the ±seconds buttons, matching keyboard shortcuts. */
export const SKIP_BACK_SECONDS = 10;
export const SKIP_FORWARD_SECONDS = 30;

export function PlayerControls({
  visible,
  playing,
  currentTime,
  duration,
  buffered,
  chapters,
  regions,
  editing,
  activeEditKind,
  onRegionEdgeChange,
  markerEditAvailable = false,
  markerEditActive = false,
  onToggleMarkerEdit,
  volume,
  muted,
  isFullscreen,
  subtitleTracks,
  activeSubtitleIndex,
  onSubtitleSelect,
  subtitleDelayMs,
  onSubtitleDelayChange,
  preferredSubtitleLanguage,
  mediaFileId,
  playerConfig,
  onRefreshSubtitles,
  sessionId,
  getSubtitleStartPosition,
  audioTracks,
  activeAudioIndex,
  onAudioSelect,
  qualityOptions,
  activeQualityId,
  isTranscoding,
  qualityError,
  onQualitySelect,
  versions,
  onSwitchVersion,
  onTogglePiP,
  showPlaybackInfo,
  onTogglePlaybackInfo,
  hasPrevEpisode = false,
  hasNextEpisode = false,
  onPrevEpisode,
  onNextEpisode,
  title,
  subtitleLabel,
  onPlayPause,
  onSeek,
  onVolumeChange,
  onMutedChange,
  onFullscreenToggle,
  onSurfaceTap,
}: PlayerControlsProps) {
  const isCoarsePointer = useCoarsePointer();
  const [overflowOpen, setOverflowOpen] = useState(false);
  const [audioOpen, setAudioOpen] = useState(false);
  const [chaptersOpen, setChaptersOpen] = useState(false);
  const safeDuration = duration > 0 ? duration : 0;
  const handleSkipBack = () => onSeek(Math.max(0, currentTime - SKIP_BACK_SECONDS));
  const handleSkipForward = () =>
    onSeek(Math.min(safeDuration || currentTime, currentTime + SKIP_FORWARD_SECONDS));
  // When playing any episode in a series (even the first or last), reserve
  // both prev/next slots so the cluster remains symmetric around the play
  // button. Movies (no episode nav at all) skip the slots entirely.
  const showEpisodeSlots = hasPrevEpisode || hasNextEpisode;

  return (
    <div
      className={`player-controls absolute inset-0 z-10 transition-opacity duration-300 ${
        visible ? "opacity-100" : "pointer-events-none opacity-0"
      }`}
      onClick={onSurfaceTap ?? onPlayPause}
    >
      {/* Gradient scrim — darkens the bottom of the frame just enough that
          white controls stay legible without washing out the picture. */}
      <div className="player-scrim pointer-events-none absolute inset-0" />

      {/* Touch transport cluster. pointer-events pass through the empty area
          so surface taps still toggle controls / double-tap-seek; only the
          cluster itself is interactive. */}
      {isCoarsePointer && (
        <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center px-[max(0.75rem,env(safe-area-inset-left))]">
          <div
            className="pointer-events-auto flex items-center gap-2"
            onClick={(event) => event.stopPropagation()}
          >
            {showEpisodeSlots ? (
              hasPrevEpisode ? (
                <CircleButton
                  size="md"
                  variant="secondary"
                  ariaLabel="Previous episode"
                  onClick={onPrevEpisode}
                >
                  <SkipBack className="h-5 w-5" fill="currentColor" />
                </CircleButton>
              ) : (
                <ClusterSlotSpacer size="md" />
              )
            ) : null}
            <CircleButton
              size="md"
              variant="secondary"
              ariaLabel={`Back ${SKIP_BACK_SECONDS} seconds`}
              onClick={handleSkipBack}
            >
              <SkipIcon direction="back" seconds={SKIP_BACK_SECONDS} />
            </CircleButton>
            <button
              type="button"
              className="flex h-16 w-16 shrink-0 items-center justify-center rounded-full bg-white text-black shadow-xl focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white"
              aria-label={playing ? "Pause" : "Play"}
              onClick={onPlayPause}
            >
              {playing ? (
                <Pause className="h-7 w-7" fill="currentColor" strokeWidth={0} />
              ) : (
                <Play className="ml-1 h-7 w-7" fill="currentColor" strokeWidth={0} />
              )}
            </button>
            <CircleButton
              size="md"
              variant="secondary"
              ariaLabel={`Forward ${SKIP_FORWARD_SECONDS} seconds`}
              onClick={handleSkipForward}
            >
              <SkipIcon direction="forward" seconds={SKIP_FORWARD_SECONDS} />
            </CircleButton>
            {showEpisodeSlots ? (
              hasNextEpisode ? (
                <CircleButton
                  size="md"
                  variant="secondary"
                  ariaLabel="Next episode"
                  onClick={onNextEpisode}
                >
                  <SkipForward className="h-5 w-5" fill="currentColor" />
                </CircleButton>
              ) : (
                <ClusterSlotSpacer size="md" />
              )
            ) : null}
          </div>
        </div>
      )}

      {/* ───── BOTTOM HUD ─────
          Three-column grid: metadata left, main playback cluster center,
          utility rail right. Seek bar spans the full width above the row
          so the playhead is always anchored to the frame edge.           */}
      <div
        className="player-hud player-rise absolute inset-x-0 bottom-0 z-10 px-[max(0.75rem,env(safe-area-inset-left))] pt-4 pb-[max(0.75rem,env(safe-area-inset-bottom))] sm:px-[max(1.5rem,env(safe-area-inset-left))] sm:pr-[max(1.5rem,env(safe-area-inset-right))] sm:pb-[max(1.25rem,env(safe-area-inset-bottom))]"
        onClick={(e) => e.stopPropagation()}
      >
        <SeekBar
          currentTime={currentTime}
          duration={duration}
          buffered={buffered}
          chapters={chapters}
          regions={regions}
          editing={editing}
          activeEditKind={activeEditKind}
          onRegionEdgeChange={onRegionEdgeChange}
          onSeek={onSeek}
        />

        {/* Grid keeps the playback cluster visually locked to the centerline
            of the frame regardless of how long the title or utility rail is.
            `minmax(0,1fr)` forces the side columns to honor 1fr behaviour
            rather than growing with their content — the cluster stays put. */}
        {isCoarsePointer ? (
          <div className="mt-2 flex min-w-0 items-center gap-2">
            <div className="flex min-w-0 flex-1 flex-col gap-0.5">
              {title ? (
                <div
                  className="truncate text-[15px] leading-tight font-semibold tracking-tight text-white"
                  title={title}
                >
                  {title}
                </div>
              ) : null}
              <div className="font-mono text-[11px] tracking-[0.12em] text-white/75 tabular-nums">
                {formatTime(currentTime)}
                <span className="mx-1 text-white/30">/</span>
                {formatTime(duration)}
              </div>
            </div>
            <SubtitleMenu
              tracks={subtitleTracks}
              preferredSubtitleLanguage={preferredSubtitleLanguage}
              activeIndex={activeSubtitleIndex}
              onSelect={onSubtitleSelect}
              delayMs={subtitleDelayMs}
              onDelayChange={onSubtitleDelayChange}
              mediaFileId={mediaFileId}
              playerConfig={playerConfig}
              onRefreshSubtitles={onRefreshSubtitles}
              sessionId={sessionId}
              getSubtitleStartPosition={getSubtitleStartPosition}
              audioTracks={audioTracks}
            />
            <QualityMenu
              options={qualityOptions}
              activeId={activeQualityId}
              isTranscoding={isTranscoding}
              error={qualityError}
              onSelect={onQualitySelect}
              versions={versions}
              onSwitchVersion={onSwitchVersion}
            />
            <button
              type="button"
              className="player-utility-btn"
              aria-label="More player options"
              aria-expanded={overflowOpen}
              aria-haspopup="menu"
              onClick={() => setOverflowOpen(true)}
            >
              <MoreHorizontal className="h-5 w-5" />
            </button>
            <button
              type="button"
              className="player-utility-btn"
              onClick={onFullscreenToggle}
              aria-label={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
            >
              {isFullscreen ? (
                <Minimize className="h-[18px] w-[18px]" />
              ) : (
                <Maximize className="h-[18px] w-[18px]" />
              )}
            </button>
          </div>
        ) : (
          <div className="mt-2 grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-3 sm:gap-5">
            {/* ─── Left: Title / episode / time ─── */}
            <div className="flex min-w-0 flex-col gap-0.5">
              {title ? (
                <div
                  className="truncate text-[15px] leading-tight font-semibold tracking-tight text-white sm:text-base"
                  title={title}
                >
                  {title}
                </div>
              ) : null}
              <div className="flex items-center gap-2 text-[10px] leading-tight text-white/55 uppercase">
                {subtitleLabel ? (
                  <>
                    <span
                      className="truncate tracking-[0.22em]"
                      style={{ maxWidth: "38ch" }}
                      title={subtitleLabel}
                    >
                      {subtitleLabel}
                    </span>
                    <span className="text-white/25">·</span>
                  </>
                ) : null}
                <span className="font-mono text-[11px] tracking-[0.12em] text-white/75 normal-case tabular-nums">
                  {formatTime(currentTime)}
                  <span className="mx-1 text-white/30">/</span>
                  {formatTime(duration)}
                </span>
              </div>
            </div>

            {/* ─── Center: Main playback cluster ─── */}
            {/* When ANY episode navigation exists (series context), both the
              prev and next slots are reserved so the play button stays
              exactly on the cluster's centerline even at the first or last
              episode. For movies the slots are omitted entirely, leaving a
              symmetric 3-button cluster that's also perfectly centered. */}
            <div className="flex items-center justify-center gap-2 sm:gap-3">
              {showEpisodeSlots ? (
                hasPrevEpisode ? (
                  <CircleButton
                    size="sm"
                    variant="secondary"
                    ariaLabel="Previous episode"
                    onClick={onPrevEpisode}
                  >
                    <SkipBack className="h-[18px] w-[18px]" fill="currentColor" />
                  </CircleButton>
                ) : (
                  <ClusterSlotSpacer size="sm" />
                )
              ) : null}

              <CircleButton
                size="sm"
                variant="secondary"
                ariaLabel={`Back ${SKIP_BACK_SECONDS} seconds`}
                onClick={handleSkipBack}
              >
                <SkipIcon direction="back" seconds={SKIP_BACK_SECONDS} />
              </CircleButton>

              <CircleButton
                size="md"
                variant="primary"
                ariaLabel={playing ? "Pause" : "Play"}
                onClick={onPlayPause}
                data-paused={!playing}
              >
                {playing ? (
                  <Pause className="h-6 w-6" strokeWidth={0} fill="currentColor" />
                ) : (
                  <Play className="ml-[2px] h-6 w-6" strokeWidth={0} fill="currentColor" />
                )}
              </CircleButton>

              <CircleButton
                size="sm"
                variant="secondary"
                ariaLabel={`Forward ${SKIP_FORWARD_SECONDS} seconds`}
                onClick={handleSkipForward}
              >
                <SkipIcon direction="forward" seconds={SKIP_FORWARD_SECONDS} />
              </CircleButton>

              {showEpisodeSlots ? (
                hasNextEpisode ? (
                  <CircleButton
                    size="sm"
                    variant="secondary"
                    ariaLabel="Next episode"
                    onClick={onNextEpisode}
                  >
                    <SkipForward className="h-[18px] w-[18px]" fill="currentColor" />
                  </CircleButton>
                ) : (
                  <ClusterSlotSpacer size="sm" />
                )
              ) : null}
            </div>

            {/* ─── Right: Utility rail ─── */}
            <div className="flex items-center justify-end gap-0.5">
              <div className="hidden sm:pointer-fine:block">
                <VolumeControl
                  volume={volume}
                  muted={muted}
                  onVolumeChange={onVolumeChange}
                  onMutedChange={onMutedChange}
                />
              </div>

              <div className="player-hud-divider mx-1 hidden sm:block" />

              {onAudioSelect && (
                <AudioTrackMenu
                  tracks={audioTracks}
                  activeIndex={activeAudioIndex}
                  onSelect={onAudioSelect}
                  currentPosition={currentTime}
                />
              )}

              <ChaptersMenu chapters={chapters ?? []} currentTime={currentTime} onSeek={onSeek} />

              <SubtitleMenu
                tracks={subtitleTracks}
                preferredSubtitleLanguage={preferredSubtitleLanguage}
                activeIndex={activeSubtitleIndex}
                onSelect={onSubtitleSelect}
                delayMs={subtitleDelayMs}
                onDelayChange={onSubtitleDelayChange}
                mediaFileId={mediaFileId}
                playerConfig={playerConfig}
                onRefreshSubtitles={onRefreshSubtitles}
                sessionId={sessionId}
                getSubtitleStartPosition={getSubtitleStartPosition}
                audioTracks={audioTracks}
              />

              <QualityMenu
                options={qualityOptions}
                activeId={activeQualityId}
                isTranscoding={isTranscoding}
                error={qualityError}
                onSelect={onQualitySelect}
                versions={versions}
                onSwitchVersion={onSwitchVersion}
              />

              {markerEditAvailable && onToggleMarkerEdit && (
                <button
                  type="button"
                  className="player-utility-btn"
                  onClick={onToggleMarkerEdit}
                  aria-label="Edit markers"
                  aria-pressed={markerEditActive}
                  title="Edit markers"
                  data-active={markerEditActive ? "true" : "false"}
                >
                  <Tags className="h-[18px] w-[18px]" />
                </button>
              )}

              <button
                type="button"
                className="player-utility-btn"
                onClick={onTogglePlaybackInfo}
                aria-label="Playback info"
                data-active={showPlaybackInfo ? "true" : "false"}
              >
                <Info className="h-[18px] w-[18px]" />
              </button>

              {onTogglePiP && document.pictureInPictureEnabled && (
                <button
                  type="button"
                  className="player-utility-btn"
                  onClick={onTogglePiP}
                  aria-label="Picture in Picture (P)"
                  title="Picture in Picture (P)"
                >
                  <PictureInPicture2 className="h-[18px] w-[18px]" />
                </button>
              )}

              <button
                type="button"
                className="player-utility-btn"
                onClick={onFullscreenToggle}
                aria-label={isFullscreen ? "Exit fullscreen" : "Fullscreen"}
              >
                {isFullscreen ? (
                  <Minimize className="h-[18px] w-[18px]" />
                ) : (
                  <Maximize className="h-[18px] w-[18px]" />
                )}
              </button>
            </div>
          </div>
        )}
      </div>

      {isCoarsePointer && overflowOpen && (
        <PlayerMenuSurface className="" onClose={() => setOverflowOpen(false)}>
          <div className="py-1">
            {onAudioSelect && audioTracks.length > 0 && (
              <OverflowAction
                icon={<AudioLines className="h-5 w-5" />}
                label="Audio tracks"
                onClick={() => {
                  setOverflowOpen(false);
                  setAudioOpen(true);
                }}
              />
            )}
            {(chapters?.length ?? 0) > 0 && (
              <OverflowAction
                icon={<ListVideo className="h-5 w-5" />}
                label="Chapters"
                onClick={() => {
                  setOverflowOpen(false);
                  setChaptersOpen(true);
                }}
              />
            )}
            <OverflowAction
              icon={<Info className="h-5 w-5" />}
              label="Playback info"
              active={showPlaybackInfo}
              onClick={() => {
                onTogglePlaybackInfo();
                setOverflowOpen(false);
              }}
            />
            {onTogglePiP && document.pictureInPictureEnabled && (
              <OverflowAction
                icon={<PictureInPicture2 className="h-5 w-5" />}
                label="Picture in Picture"
                onClick={() => {
                  onTogglePiP();
                  setOverflowOpen(false);
                }}
              />
            )}
            {markerEditAvailable && onToggleMarkerEdit && (
              <OverflowAction
                icon={<Tags className="h-5 w-5" />}
                label="Edit markers"
                active={markerEditActive}
                onClick={() => {
                  onToggleMarkerEdit();
                  setOverflowOpen(false);
                }}
              />
            )}
          </div>
        </PlayerMenuSurface>
      )}
      {isCoarsePointer && onAudioSelect && (
        <AudioTrackMenu
          tracks={audioTracks}
          activeIndex={activeAudioIndex}
          onSelect={onAudioSelect}
          currentPosition={currentTime}
          open={audioOpen}
          onOpenChange={setAudioOpen}
          hideTrigger
        />
      )}
      {isCoarsePointer && (
        <ChaptersMenu
          chapters={chapters ?? []}
          currentTime={currentTime}
          onSeek={onSeek}
          open={chaptersOpen}
          onOpenChange={setChaptersOpen}
          hideTrigger
        />
      )}
    </div>
  );
}

function OverflowAction({
  icon,
  label,
  active = false,
  onClick,
}: {
  icon: ReactNode;
  label: string;
  active?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      role="menuitem"
      type="button"
      className={`flex min-h-12 w-full items-center gap-3 px-5 py-3 text-left text-sm ${active ? "bg-white/10 text-white" : "text-white/80"}`}
      onClick={onClick}
    >
      {icon}
      <span>{label}</span>
    </button>
  );
}

/* ─────────────────────────────────────────────────────────────────────
   Internal building blocks
   ───────────────────────────────────────────────────────────────────── */

/** Invisible placeholder that reserves the exact footprint of a CircleButton
 *  so the playback cluster stays symmetric when a neighboring episode isn't
 *  available (first/last episode in a series). */
function ClusterSlotSpacer({ size }: { size: "sm" | "md" }) {
  const sizing = size === "md" ? "h-12 w-12 sm:h-14 sm:w-14" : "h-10 w-10 sm:h-11 sm:w-11";
  return <div aria-hidden="true" className={sizing} />;
}

/** Curved arrow with the skip-seconds number centered in the loop. */
function SkipIcon({ direction, seconds }: { direction: "back" | "forward"; seconds: number }) {
  const Arrow = direction === "back" ? RotateCcw : RotateCw;
  return (
    <span className="relative flex h-7 w-7 items-center justify-center">
      <Arrow className="h-7 w-7" strokeWidth={1.6} />
      <span className="absolute inset-0 flex items-center justify-center pb-[1px] text-[8.5px] font-semibold tracking-tight tabular-nums">
        {seconds}
      </span>
    </span>
  );
}
