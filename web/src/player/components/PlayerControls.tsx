import { useLayoutEffect, useRef, useState, type MouseEvent } from "react";
import {
  AudioLines,
  ListVideo,
  Maximize,
  Minimize,
  MoreHorizontal,
  Pause,
  Play,
  SkipBack,
  SkipForward,
} from "lucide-react";
import "@/styles/player-hud.css";
import { findActiveChapterIndex } from "../utils/chapters";
import { SeekBar } from "./SeekBar";
import { formatTime } from "../utils/formatTime";
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
  VideoFitMode,
} from "../types";
import type { PlayerTrickplay } from "../trickplay";
import type { VersionInfo } from "./QualityMenu";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import type { SubtitleSync } from "../hooks/useSubtitleSync";
import { useCoarsePointer } from "../hooks/useCoarsePointer";
import { PlayerMenuSurface } from "./PlayerMenuSurface";
import { portraitDock } from "../utils/portraitDock";
import { AmbientLetterbox, type AmbientSource } from "./AmbientLetterbox";
import {
  MoreMenu,
  MoreMenuItems,
  OverflowAction,
  SkipGlyph,
  TitleBlock,
  TransportButton,
} from "./PlayerControlsParts";
import { useT } from "@/i18n";
import { usePlayerEndTime } from "../hooks/usePlayerEndTime";

interface PlayerControlsProps {
  /** The profile's rewind/fast-forward intervals, shown on the transport buttons. */
  skipSeconds: { back: number; forward: number };
  /** Directional skips; the player owns the timeline math (offsets, pending seeks). */
  onSkip: { back: () => void; forward: () => void };
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
  // Seek bar previews
  trickplay?: PlayerTrickplay | null;
  trickplayUpdatedAt?: number;
  onTrickplayError?: () => void;
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
  videoFit: VideoFitMode;
  /** The video's width / height; places the touch dock under a letterboxed picture. */
  videoAspect?: number;
  /** The element showing the picture, for the upright-phone letterbox glow. */
  getAmbientSource?: () => AmbientSource | null;

  onVideoFitToggle: () => void;
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
  onSubtitleJobAccepted?: (jobId: string, resultSubtitleId?: string) => void;
  sessionId?: string;
  getSubtitleStartPosition?: () => number;
  subtitleSync?: SubtitleSync;
  // Audio
  audioTracks: PlayerAudioTrack[];
  activeAudioIndex: number;
  onAudioSelect?: (index: number, currentPosition: number) => void;
  // Quality
  qualityOptions: QualityOption[];
  activeQualityId: string;
  /** The plan's `active_quality`, the menu entry it delivers. */
  activeQuality?: string;
  isTranscoding: boolean;
  qualityError: string | null;
  onQualitySelect: (id: string) => void;
  // Version switching
  versions?: VersionInfo[];
  versionLocked?: boolean;
  onSwitchVersion?: (fileId: number) => void;
  // Speed. Omitted while something else owns the rate (a Watch Party room).
  playbackSpeed?: number;
  onPlaybackSpeedChange?: (rate: number) => void;
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
  /** Absent when fullscreen cannot work, such as engine playback on iPhone. */
  onFullscreenToggle?: () => void;
  onSurfaceTap?: (event: MouseEvent<HTMLElement>) => void;
}

/**
 * Below this width (in rem, so large text counts) the utility rail no longer
 * fits beside the transport: secondary controls move into More and the
 * transport moves over the picture.
 */
const FULL_LAYOUT_MIN_REM = 64;

/** .hud-transport-btn's width. */
const TRANSPORT_BUTTON_REM = 3;
/** The docked transport's sizings, largest first; play is .hud-play-btn(--large). */
const DOCK_TRANSPORT_SIZINGS = [
  { large: true, playRem: 4.5, gapRem: 1.25, gapClass: "gap-5" },
  { large: false, playRem: 3.75, gapRem: 0.75, gapClass: "gap-3" },
  { large: false, playRem: 3.75, gapRem: 0.375, gapClass: "gap-1.5" },
] as const;

export function PlayerControls({
  skipSeconds,
  onSkip,
  visible,
  playing,
  currentTime,
  duration,
  buffered,
  chapters,
  regions,
  trickplay,
  trickplayUpdatedAt,
  onTrickplayError,
  editing,
  activeEditKind,
  onRegionEdgeChange,
  markerEditAvailable = false,
  markerEditActive = false,
  onToggleMarkerEdit,
  volume,
  muted,
  isFullscreen,
  videoFit,
  videoAspect = 0,
  getAmbientSource,
  onVideoFitToggle,
  subtitleTracks,
  activeSubtitleIndex,
  onSubtitleSelect,
  subtitleDelayMs,
  onSubtitleDelayChange,
  preferredSubtitleLanguage,
  mediaFileId,
  playerConfig,
  onRefreshSubtitles,
  onSubtitleJobAccepted,
  sessionId,
  getSubtitleStartPosition,
  subtitleSync,
  audioTracks,
  activeAudioIndex,
  onAudioSelect,
  qualityOptions,
  activeQualityId,
  activeQuality,
  isTranscoding,
  qualityError,
  onQualitySelect,
  versions,
  versionLocked,
  onSwitchVersion,
  playbackSpeed,
  onPlaybackSpeedChange,
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
  const t = useT();
  const isCoarsePointer = useCoarsePointer();
  const controlsRef = useRef<HTMLDivElement>(null);
  const remProbeRef = useRef<HTMLSpanElement>(null);
  const dockRef = useRef<HTMLDivElement>(null);
  const [size, setSize] = useState({ width: 0, height: 0, rem: 16, dock: 0 });
  const narrowPlayer = size.width > 0 && size.width < FULL_LAYOUT_MIN_REM * size.rem;
  const compactControls = isCoarsePointer || narrowPlayer;
  // The finish time follows the bar while it is dragged.
  const [scrubTime, setScrubTime] = useState<number | null>(null);
  const endsAt = usePlayerEndTime(scrubTime ?? currentTime, duration, playbackSpeed ?? 1, visible);

  useLayoutEffect(() => {
    const element = controlsRef.current;
    if (!element || typeof ResizeObserver === "undefined") return;
    const measure = () => {
      const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
      setSize({
        width: element.clientWidth,
        height: element.clientHeight,
        rem,
        dock: dockRef.current?.offsetHeight ?? 0,
      });
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    // A 1rem probe resizes when the reader changes their text size, which
    // moves the layout threshold without resizing the player.
    if (remProbeRef.current) observer.observe(remProbeRef.current);
    if (dockRef.current) observer.observe(dockRef.current);
    return () => observer.disconnect();
  }, []);
  const [overflowOpen, setOverflowOpen] = useState(false);
  const [audioOpen, setAudioOpen] = useState(false);
  const [chaptersOpen, setChaptersOpen] = useState(false);
  // Discard compact-only menus when switching layouts so they cannot
  // reappear after a resize or fullscreen round trip.
  const [menusLayout, setMenusLayout] = useState(compactControls);
  if (menusLayout !== compactControls) {
    setMenusLayout(compactControls);
    setOverflowOpen(false);
    setAudioOpen(false);
    setChaptersOpen(false);
  }
  // When playing any episode in a series (even the first or last), reserve
  // both prev/next slots so the cluster remains symmetric around the play
  // button. Movies (no episode nav at all) skip the slots entirely.
  const showEpisodeSlots = hasPrevEpisode || hasNextEpisode;
  // The upright-phone dock's transport row cannot wrap or shrink, so it takes
  // the largest sizing that fits across the player (less the dock's 1rem
  // sides): the large play button with wide gaps, then standard sizes, then
  // tight gaps.
  const dockSlots = showEpisodeSlots ? 5 : 3;
  const dockSizing =
    DOCK_TRANSPORT_SIZINGS.find(
      ({ playRem, gapRem }) =>
        ((dockSlots - 1) * (TRANSPORT_BUTTON_REM + gapRem) + playRem + 2) * size.rem <= size.width,
    ) ?? DOCK_TRANSPORT_SIZINGS[DOCK_TRANSPORT_SIZINGS.length - 1]!;
  const pipAvailable = Boolean(onTogglePiP) && document.pictureInPictureEnabled;

  const activeChapterIndex = findActiveChapterIndex(chapters ?? [], currentTime);
  const chapterTitle =
    chapters && chapters.length > 1 && activeChapterIndex >= 0
      ? chapters[activeChapterIndex]?.title
      : undefined;
  const titleProps = { title, subtitleLabel, chapterTitle, endsAt };
  const remaining = Math.max(0, duration - currentTime);

  // A phone held upright letterboxes the picture into a band mid-screen. The
  // HUD then gathers around that band: the title just above it and the
  // controls, transport included, just below it.
  const dock =
    isCoarsePointer && videoFit === "contain"
      ? portraitDock(size.width, size.height, videoAspect, size.rem)
      : null;
  const dockTop = dock ? dock.pictureBottom + 0.75 * size.rem : null;
  const portraitTitleBottom = dock ? size.height - dock.pictureTop + 0.75 * size.rem : null;

  // A phone on its side: the title shares the control row, so the dock stays
  // short enough for the transport to sit at the screen's vertical centre.
  const landscapeTouch = isCoarsePointer && dockTop === null && size.width > size.height;

  // A short touch player (landscape phone, large text) has no room above the
  // dock for the floating transport; it joins the dock's control row instead.
  const inlineTransport =
    isCoarsePointer &&
    dockTop === null &&
    size.dock > 0 &&
    size.height - 4 * size.rem - size.dock < 5.5 * size.rem;

  // The floating touch transport centres on the screen when it clears both
  // the top bar (4rem) and the dock; otherwise it centres in the gap between.
  const halfTransport = 1.875 * size.rem;
  const transportCentred =
    size.height / 2 - halfTransport >= 4 * size.rem &&
    size.height / 2 + halfTransport + 0.5 * size.rem <= size.height - size.dock;

  // A centred cluster reserves both episode slots so play stays on the
  // centreline; a left-aligned row shows only the buttons that apply.
  const transport = (large: boolean, reserveSlots = true) => (
    <>
      {showEpisodeSlots ? (
        hasPrevEpisode ? (
          <TransportButton label={t("web.player.controls.previousEpisode")} onClick={onPrevEpisode}>
            <SkipBack fill="currentColor" strokeWidth={0} />
          </TransportButton>
        ) : reserveSlots ? (
          <span aria-hidden="true" className="hud-transport-btn" />
        ) : null
      ) : null}
      <TransportButton
        label={t("web.player.controls.skipBack", { count: skipSeconds.back })}
        onClick={onSkip.back}
      >
        <SkipGlyph direction="back" seconds={skipSeconds.back} />
      </TransportButton>
      <button
        type="button"
        className={`hud-play-btn ${large ? "hud-play-btn--large" : ""}`}
        aria-label={playing ? t("web.player.controls.pause") : t("web.player.controls.play")}
        onClick={onPlayPause}
      >
        {playing ? (
          <Pause fill="currentColor" strokeWidth={0} />
        ) : (
          <Play className="translate-x-[0.0625rem]" fill="currentColor" strokeWidth={0} />
        )}
      </button>
      <TransportButton
        label={t("web.player.controls.skipForward", { count: skipSeconds.forward })}
        onClick={onSkip.forward}
      >
        <SkipGlyph direction="forward" seconds={skipSeconds.forward} />
      </TransportButton>
      {showEpisodeSlots ? (
        hasNextEpisode ? (
          <TransportButton label={t("web.player.controls.nextEpisode")} onClick={onNextEpisode}>
            <SkipForward fill="currentColor" strokeWidth={0} />
          </TransportButton>
        ) : reserveSlots ? (
          <span aria-hidden="true" className="hud-transport-btn" />
        ) : null
      ) : null}
    </>
  );

  const subtitleMenu = (
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
      onSubtitleJobAccepted={onSubtitleJobAccepted}
      sessionId={sessionId}
      getSubtitleStartPosition={getSubtitleStartPosition}
      subtitleSync={subtitleSync}
      audioTracks={audioTracks}
    />
  );
  const qualityMenu = (
    <QualityMenu
      options={qualityOptions}
      activeId={activeQualityId}
      activeQuality={activeQuality}
      isTranscoding={isTranscoding}
      error={qualityError}
      onSelect={onQualitySelect}
      versions={versions}
      versionLocked={versionLocked}
      onSwitchVersion={onSwitchVersion}
      playbackSpeed={playbackSpeed}
      onPlaybackSpeedChange={onPlaybackSpeedChange}
    />
  );
  const fullscreenButton = onFullscreenToggle ? (
    <button
      type="button"
      className="hud-icon-btn"
      onClick={onFullscreenToggle}
      aria-label={
        isFullscreen ? t("web.player.controls.exitFullscreen") : t("web.player.controls.fullscreen")
      }
      title={
        isFullscreen
          ? t("web.player.controls.exitFullscreenShortcut")
          : t("web.player.controls.fullscreenShortcut")
      }
    >
      {isFullscreen ? <Minimize /> : <Maximize />}
    </button>
  ) : null;

  return (
    <div
      ref={controlsRef}
      data-compact={compactControls}
      data-touch={isCoarsePointer}
      className={`player-controls hud absolute inset-0 z-10 transition-opacity duration-300 ${
        visible ? "opacity-100" : "pointer-events-none opacity-0"
      }`}
      onClick={onSurfaceTap ?? onPlayPause}
    >
      <span
        ref={remProbeRef}
        aria-hidden="true"
        className="pointer-events-none invisible absolute h-px w-[1rem]"
      />
      {dock && getAmbientSource ? (
        <AmbientLetterbox
          getSource={getAmbientSource}
          pictureTop={dock.pictureTop}
          pictureBottom={dock.pictureBottom}
          height={size.height}
          active={visible}
        />
      ) : null}
      {/* Scrims darken only where controls sit, so text stays legible on a
          white frame without dimming the middle of the picture. */}
      <div
        aria-hidden="true"
        className="hud-scrim-top pointer-events-none absolute inset-x-0 top-0"
      />
      <div
        aria-hidden="true"
        className="hud-scrim-bottom pointer-events-none absolute inset-x-0 bottom-0"
      />

      {/* Touch and narrow players: transport over the picture. pointer-events
          pass through the empty area so surface taps still toggle controls /
          double-tap-seek; only the cluster itself is interactive. */}
      {compactControls && !inlineTransport && dockTop === null && (
        <div
          className="player-compact-transport pointer-events-none absolute inset-0 z-10 flex items-center justify-center px-[max(0.75rem,env(safe-area-inset-left))]"
          // On touch the transport centres on the screen; when that would
          // reach the dock, it centres between the top bar and the dock.
          style={
            isCoarsePointer && dockTop === null && size.dock > 0 && !transportCentred
              ? { top: `${4 * size.rem}px`, bottom: `${size.dock}px` }
              : undefined
          }
        >
          {/* Transport reads left to right in every language, like the timeline. */}
          <div
            dir="ltr"
            className={`flex items-center gap-3 sm:gap-5 ${visible ? "pointer-events-auto" : ""}`}
            onClick={(event) => event.stopPropagation()}
          >
            {transport(false)}
          </div>
        </div>
      )}

      {portraitTitleBottom !== null ? (
        <div
          className="hud-portrait-title pointer-events-none absolute inset-x-0 z-10 flex px-[max(1.25rem,env(safe-area-inset-left))]"
          style={{ bottom: portraitTitleBottom }}
        >
          <TitleBlock {...titleProps} />
        </div>
      ) : null}

      <div
        ref={dockRef}
        data-docked={dockTop !== null ? "true" : undefined}
        style={dockTop !== null ? { top: dockTop, bottom: "auto" } : undefined}
        className="player-hud absolute inset-x-0 bottom-0 z-10 pt-4 pr-[max(1rem,env(safe-area-inset-right))] pb-[max(0.75rem,env(safe-area-inset-bottom))] pl-[max(1rem,env(safe-area-inset-left))] sm:pr-[max(2rem,env(safe-area-inset-right))] sm:pb-[max(2rem,env(safe-area-inset-bottom))] sm:pl-[max(2rem,env(safe-area-inset-left))]"
        onClick={(e) => e.stopPropagation()}
      >
        {/* On touch the title gets its own line; the row below is all controls. */}
        {isCoarsePointer && dockTop === null && !landscapeTouch ? (
          <div className="hud-touch-title mb-1 flex px-1">
            <TitleBlock {...titleProps} />
          </div>
        ) : null}
        {/* Timeline, left to right in every language. Compact layouts flank the
            bar with elapsed and remaining time; the full layout's times sit
            with the title under it. */}
        <div dir="ltr" className="flex items-center gap-2 sm:gap-3">
          {compactControls ? (
            <span className="hud-time text-end">{formatTime(currentTime)}</span>
          ) : null}
          <div className="min-w-0 flex-1">
            <SeekBar
              currentTime={currentTime}
              duration={duration}
              buffered={buffered}
              chapters={chapters}
              regions={regions}
              trickplay={trickplay}
              trickplayUpdatedAt={trickplayUpdatedAt}
              onTrickplayError={onTrickplayError}
              editing={editing}
              activeEditKind={activeEditKind}
              onRegionEdgeChange={onRegionEdgeChange}
              onSeek={onSeek}
              onSkip={onSkip}
              onScrubTimeChange={setScrubTime}
            />
          </div>
          {compactControls ? (
            <span className="hud-time">
              <span className="sr-only">
                {t("web.player.controls.remainingTimeLabel", { time: formatTime(remaining) })}
              </span>
              <span aria-hidden="true">
                {t("web.player.controls.remainingTime", { time: formatTime(remaining) })}
              </span>
            </span>
          ) : null}
        </div>

        {dockTop !== null ? (
          <div dir="ltr" className={`mt-2 flex items-center justify-center ${dockSizing.gapClass}`}>
            {transport(dockSizing.large)}
          </div>
        ) : null}

        {compactControls ? (
          <div
            className={`player-compact-row mt-1 flex min-w-0 flex-wrap items-center gap-1 ${
              dockTop !== null ? "justify-center gap-3" : "justify-end"
            }`}
          >
            {inlineTransport ? (
              // The margin follows the row's direction; the transport inside
              // reads left to right.
              <div className="me-auto">
                <div dir="ltr" className="hud-inline-transport flex items-center gap-2">
                  {transport(false)}
                </div>
              </div>
            ) : isCoarsePointer && !landscapeTouch ? (
              dockTop === null ? (
                <span className="flex-1" />
              ) : null
            ) : (
              <TitleBlock {...titleProps} />
            )}
            {subtitleMenu}
            {qualityMenu}
            <button
              type="button"
              className="hud-icon-btn"
              aria-label={t("web.player.controls.moreOptions")}
              aria-expanded={overflowOpen}
              aria-haspopup="menu"
              onClick={() => setOverflowOpen(true)}
            >
              <MoreHorizontal />
            </button>
            {fullscreenButton}
          </div>
        ) : (
          <div className="player-full-row mt-3 grid grid-cols-[minmax(0,1fr)_auto_minmax(max-content,1fr)] items-center gap-6">
            {/* What is playing at the start, the transport on the centreline,
                settings at the end. When the settings need more room the title
                gives it up first. The transport reads left to right in every
                language. */}
            <TitleBlock
              {...titleProps}
              times={{
                elapsed: formatTime(currentTime),
                total: duration > 0 ? formatTime(duration) : undefined,
              }}
            />
            <div dir="ltr" className="flex items-center gap-3">
              {transport(false)}
            </div>

            <div className="flex items-center justify-end gap-1.5">
              <VolumeControl
                tone="hud"
                volume={volume}
                muted={muted}
                onVolumeChange={onVolumeChange}
                onMutedChange={onMutedChange}
              />
              <span aria-hidden="true" className="mx-1.5 h-6 w-px bg-white/15" />
              {onAudioSelect && (
                <AudioTrackMenu
                  tracks={audioTracks}
                  activeIndex={activeAudioIndex}
                  onSelect={onAudioSelect}
                  currentPosition={currentTime}
                />
              )}
              {subtitleMenu}
              <ChaptersMenu chapters={chapters ?? []} currentTime={currentTime} onSeek={onSeek} />
              <span aria-hidden="true" className="mx-1.5 h-6 w-px bg-white/15" />
              {qualityMenu}
              <MoreMenu
                open={overflowOpen}
                onOpenChange={setOverflowOpen}
                pipAvailable={pipAvailable}
                onTogglePiP={onTogglePiP}
                videoFit={videoFit}
                onVideoFitToggle={onVideoFitToggle}
                showPlaybackInfo={showPlaybackInfo}
                onTogglePlaybackInfo={onTogglePlaybackInfo}
                markerEditAvailable={markerEditAvailable}
                markerEditActive={markerEditActive}
                onToggleMarkerEdit={onToggleMarkerEdit}
              />
              {fullscreenButton}
            </div>
          </div>
        )}
      </div>

      {compactControls && overflowOpen && !isCoarsePointer && (
        <button
          type="button"
          className="absolute inset-0 z-40"
          aria-label={t("web.player.menu.close")}
          onClick={(event) => {
            event.stopPropagation();
            setOverflowOpen(false);
          }}
        />
      )}
      {compactControls && overflowOpen && (
        <PlayerMenuSurface
          title={t("web.player.controls.more")}
          onClose={() => setOverflowOpen(false)}
        >
          {!isCoarsePointer && (
            <div className="flex items-center justify-between gap-4 px-4 py-1.5 text-sm">
              <span>{t("web.player.controls.volume")}</span>
              <VolumeControl
                tone="hud"
                volume={volume}
                muted={muted}
                onVolumeChange={onVolumeChange}
                onMutedChange={onMutedChange}
              />
            </div>
          )}
          {onAudioSelect && audioTracks.length > 0 && (
            <OverflowAction
              icon={<AudioLines />}
              label={t("web.player.audioMenu.trigger")}
              onClick={() => {
                setOverflowOpen(false);
                setAudioOpen(true);
              }}
            />
          )}
          {(chapters?.length ?? 0) > 0 && (
            <OverflowAction
              icon={<ListVideo />}
              label={t("web.player.chaptersMenu.trigger")}
              onClick={() => {
                setOverflowOpen(false);
                setChaptersOpen(true);
              }}
            />
          )}
          <MoreMenuItems
            close={() => setOverflowOpen(false)}
            pipAvailable={pipAvailable}
            onTogglePiP={onTogglePiP}
            videoFit={videoFit}
            onVideoFitToggle={onVideoFitToggle}
            showPlaybackInfo={showPlaybackInfo}
            onTogglePlaybackInfo={onTogglePlaybackInfo}
            markerEditAvailable={markerEditAvailable}
            markerEditActive={markerEditActive}
            onToggleMarkerEdit={onToggleMarkerEdit}
          />
        </PlayerMenuSurface>
      )}
      {compactControls && onAudioSelect && (
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
      {compactControls && (
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
