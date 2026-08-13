import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { ParsedCue } from "../utils/parseVTT";
import { resolveSubtitleAutoSelect } from "../utils/subtitleSort";
import type HlsType from "hls.js";
import { PlayerControls } from "./PlayerControls";
import { PlaybackInfoOverlay } from "./PlaybackInfoOverlay";
import { PlaybackNoticeOverlay } from "./PlaybackNoticeOverlay";
import { IntroSkipButton } from "./IntroSkipButton";
import { MarkerEditPanel } from "./MarkerEditPanel";
import { NextEpisodeOverlay } from "./NextEpisodeOverlay";
import { usePlaybackRealtime } from "../hooks/usePlaybackRealtime";
import { useWatchProgress } from "../hooks/useWatchProgress";
import { useKeyboardShortcuts } from "../hooks/useKeyboardShortcuts";
import { useRemuxSeeking } from "../hooks/useRemuxSeeking";
import { useSubtitleTracks } from "../hooks/useSubtitleTracks";
import { useASSSubtitles } from "../hooks/useASSSubtitles";
import { useSubtitleAppearance } from "../hooks/useSubtitleAppearance";
import { useSubtitleLayout } from "../hooks/useSubtitleLayout";
import { computeSubtitleFontSize } from "@/lib/subtitleAppearance";
import { useNextEpisode } from "../hooks/useNextEpisode";
import { MARKER_KINDS, useMarkerEditor } from "../hooks/useMarkerEditor";
import { useWatchTogetherPlaybackSync } from "../hooks/useWatchTogetherPlaybackSync";
import type { WatchTogetherRoomConnectionResult } from "../hooks/useWatchTogetherRoomConnection";
import { getPersistedVolume, persistVolume } from "./VolumeControl";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { qualityOptionsFromPlanV3 } from "../playback-info";
import { preconnectToStreamOrigin } from "../stream-url";
import { WatchTogetherPanel } from "./WatchTogetherPanel";
import type {
  PlaybackRealtimeCommandEnvelope,
  PlaybackRealtimeEventEnvelope,
} from "../realtime-protocol";
import { resolvePendingSeekTime } from "../utils/pendingSeek";
import { resolveVersionAudioLanguage } from "../utils/effectiveAudioLanguage";
import { HlsStartupGuard } from "../utils/hlsStartupGuard";
import { normalizeSubtitleMode } from "../utils/subtitleMode";
import type {
  PlaybackExitState,
  PlayerDisplayMode,
  PlayerPictureInPictureChange,
  PlayerPlaybackStateChange,
  PlayerPlaybackTransport,
  PlayerAudioTrack,
  PlayerChapter,
  PlayerFileVersion,
  PlayerSubtitleInfo,
  PlayerSubtitleTrackSignature,
  PlayerTimeRange,
  MarkerDraft,
  MarkerRegionView,
  SeriesContext,
  SubtitleMode,
} from "../types";
import type { FailureV3, PlanV3, SubtitleInventoryItemV3 } from "../protocol-v3";
import {
  mediaDurationSeconds,
  subtitleStartPositionSeconds,
  toMediaTime,
  toPlayerTime,
} from "../utils/mediaTimeline";
import { pendingServerSubtitleSelection } from "../utils/playableSubtitles";
import {
  copyWatchTogetherInvite,
  endWatchTogetherRoom,
  setWatchTogetherGuestControl,
} from "@/lib/watchTogetherActions";
import { toast } from "sonner";

// Reserved index for the in-progress live AI translation track. Sits well above
// any real subtitle index so it never collides.
const LIVE_SUBTITLE_INDEX = 1_000_000;
// Resume playback once translated cues cover at least this far ahead of the
// playhead; a hard cap also resumes so we never wait forever.
const TRANSLATION_RESUME_TIMEOUT_MS = 30_000;

interface VideoPlayerProps {
  title: string;
  year?: number;
  streamUrl: string;
  /**
   * The server's plan for this session. Everything about *how* the media plays —
   * the transport, the timeline, the codecs, the quality menu — is read from
   * here rather than derived locally.
   */
  plan: PlanV3;
  /** Bumped on every adopted plan; stream-reload effects key on it. */
  planRevision: number;
  /** Whether a newly adopted transport should begin playing immediately. */
  shouldAutoPlay?: boolean;
  /** True while a replan is in flight, so the quality menu can show progress. */
  replanning?: boolean;
  /** Server-described replan error, if the last replan was refused. */
  replanError?: string | null;
  /** Title for the replan error, used when surfacing the refusal as a toast. */
  replanErrorTitle?: string | null;
  sessionId: string;
  selectedVersion?: PlayerFileVersion;
  versions?: PlayerFileVersion[];
  activeFileId?: number | null;
  chapters?: PlayerChapter[];
  onSwitchVersion?: (fileId: number, currentPosition: number) => void;
  subtitleUrls: PlayerSubtitleInfo[];
  initialPosition: number;
  /** `quality_change` replan for a label taken from `plan.available_qualities`. */
  onQualitySelect?: (label: string, currentPosition: number) => void;
  /** `track_change` replan for a subtitle the server has to render. */
  onSubtitleTrackChange?: (combinedIndex: number | null, currentPosition: number) => void;
  /** `failure_recovery` replan after the client could not play the plan. */
  onPlanFailure?: (failure: FailureV3, currentPosition: number) => void;
  /** `seek_reanchor` replan when a seek target falls outside the seekable window. */
  onReanchorSeek?: (positionSeconds: number) => void;
  preferredSubtitleLanguage?: string | null;
  preferredSubtitleTrackSignature?: PlayerSubtitleTrackSignature | null;
  subtitleMode?: SubtitleMode;
  showForcedSubtitles?: boolean;
  profileLanguage?: string | null;
  intro: PlayerTimeRange | null;
  autoSkipIntro?: boolean;
  credits: PlayerTimeRange | null;
  recap?: PlayerTimeRange | null;
  autoSkipRecap?: boolean;
  preview?: PlayerTimeRange | null;
  autoPlayNextPreview?: boolean;
  canEditMarkers?: boolean;
  /** Notified after a successful in-player marker edit so the host can patch local state. */
  onMarkersEdited?: (fileId: number, markers: MarkerDraft) => void;
  duration?: number;
  seriesContext?: SeriesContext;
  onNavigateEpisode?: (contentId: string) => void;
  /** The session's current quality preference, as the server normalized it. */
  qualityPreference: string;
  onRefreshSubtitles?: (currentPosition: number) => void;
  /** Folds a realtime-delivered inventory entry in at the server's ordinal. */
  onApplySubtitleTrack?: (track: SubtitleInventoryItemV3) => void;
  audioTracks?: PlayerAudioTrack[];
  activeAudioIndex?: number;
  onAudioSelect?: (index: number, currentPosition: number) => void;
  onSubtitleChanged?: (index: number | null, inventoryTrack?: SubtitleInventoryItemV3) => void;
  onExit: (state?: PlaybackExitState) => void | Promise<void>;
  onMinimize?: (state?: PlaybackExitState) => void | Promise<void>;
  onEnded?: () => void;
  displayMode?: PlayerDisplayMode;
  onPictureInPictureChange?: (change: PlayerPictureInPictureChange) => void;
  autoEnterPictureInPicture?: boolean;
  onPlaybackStateChange?: (state: PlayerPlaybackStateChange) => void;
  onPlaybackTransportReady?: (transport: PlayerPlaybackTransport | null) => void;
  onReturnFromPostRoll?: () => void;
  onRealtimeEvent?: (event: PlaybackRealtimeEventEnvelope) => void;
  onRealtimeConnectionStateChange?: (state: "disconnected" | "connecting" | "connected") => void;
  watchTogetherRoomId?: string | null;
  watchTogetherConnection?: WatchTogetherRoomConnectionResult;
}

/** Preload hls.js eagerly so it's cached before the first transcode. */
const hlsPromise: Promise<typeof HlsType> = import("hls.js").then((m) => m.default);
const EXIT_PROGRESS_FLUSH_TIMEOUT_MS = 1_000;
const FIREFOX_COMPATIBILITY_FALLBACK_DELAY_MS = 8_000;

interface PlaybackNoticeState {
  title?: string;
  message: string;
  tone: "info" | "warning";
}

function readNumericPayload(
  payload: Record<string, unknown> | undefined,
  ...keys: string[]
): number | null {
  for (const key of keys) {
    const value = payload?.[key];
    if (typeof value === "number" && Number.isFinite(value)) {
      return value;
    }
  }
  return null;
}

function readStringPayload(
  payload: Record<string, unknown> | undefined,
  ...keys: string[]
): string | null {
  for (const key of keys) {
    const value = payload?.[key];
    if (typeof value === "string" && value.trim() !== "") {
      return value;
    }
  }
  return null;
}

export function VideoPlayer({
  title,
  year,
  streamUrl,
  plan,
  planRevision,
  shouldAutoPlay = true,
  replanning = false,
  replanError = null,
  replanErrorTitle = null,
  sessionId,
  selectedVersion,
  versions = [],
  activeFileId,
  chapters = [],
  onSwitchVersion,
  subtitleUrls,
  initialPosition,
  onQualitySelect,
  onSubtitleTrackChange,
  onPlanFailure,
  onReanchorSeek,
  preferredSubtitleLanguage,
  preferredSubtitleTrackSignature,
  subtitleMode,
  showForcedSubtitles,
  profileLanguage,
  intro,
  autoSkipIntro = false,
  credits,
  recap = null,
  autoSkipRecap = false,
  preview = null,
  autoPlayNextPreview = false,
  canEditMarkers = true,
  onMarkersEdited,
  duration: propDuration,
  seriesContext,
  onNavigateEpisode,
  qualityPreference,
  onRefreshSubtitles,
  onApplySubtitleTrack,
  audioTracks = [],
  activeAudioIndex = 0,
  onAudioSelect,
  onSubtitleChanged,
  onExit,
  onMinimize,
  onEnded,
  displayMode = "foreground",
  onPictureInPictureChange,
  autoEnterPictureInPicture = false,
  onPlaybackStateChange,
  onPlaybackTransportReady,
  onReturnFromPostRoll,
  onRealtimeEvent,
  onRealtimeConnectionStateChange,
  watchTogetherRoomId,
  watchTogetherConnection,
}: VideoPlayerProps) {
  const playerConfig = usePlayerConfig();
  const isDetached = displayMode !== "foreground";

  // Refs
  const videoRef = useRef<HTMLVideoElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const isMountedRef = useRef(true);
  const hlsRef = useRef<HlsType | null>(null);
  const hlsStartupGuardRef = useRef<HlsStartupGuard | null>(null);
  const mediaRecoveryAttemptsRef = useRef(0);
  const lastRecoveryRef = useRef(0);
  const reportedPlanFailureKeyRef = useRef<string | null>(null);
  const transportFailedForPlanRevisionRef = useRef<number | null>(null);
  const timelineOffsetRef = useRef(0);
  const subtitleFetchAnchorRef = useRef(initialPosition);
  const backendDurationRef = useRef(propDuration ?? 0);
  const autoEnterPictureInPictureAttemptedRef = useRef(false);
  const autoSkippedIntroKeyRef = useRef<string | null>(null);
  const autoSkippedRecapKeyRef = useRef<string | null>(null);
  const endedFiredRef = useRef(false);
  const [hasEnded, setHasEnded] = useState(false);
  const onEndedRef = useRef(onEnded);
  const currentTimeRef = useRef(0);
  const durationRef = useRef(propDuration ?? 0);
  const compatibilityFallbackKeyRef = useRef<string | null>(null);
  const lastRoomCommandIdRef = useRef<string | null>(null);
  const roomCommandTimerRef = useRef<number | null>(null);
  const performPlayerSeekRef = useRef<(seconds: number) => void>(() => {});
  const reportRoomReadyRef = useRef<
    (positionSeconds?: number, isPaused?: boolean) => { ok: boolean }
  >(() => ({ ok: false }));

  // Playback state
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [pendingSeekTime, setPendingSeekTime] = useState<number | null>(null);
  const [duration, setDuration] = useState(propDuration ?? 0);
  const [buffered, setBuffered] = useState<TimeRanges | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [buffering, setBuffering] = useState(false);
  const bufferingTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [awaitingFirstFrame, setAwaitingFirstFrame] = useState(true);
  const [isLeaving, setIsLeaving] = useState(false);
  const leaveInProgressRef = useRef(false);
  const [notice, setNotice] = useState<PlaybackNoticeState | null>(null);

  // Volume (persisted via localStorage)
  const [volume, setVolume] = useState(() => getPersistedVolume().volume);
  const [muted, setMuted] = useState(() => getPersistedVolume().muted);

  // Subtitles
  const [activeSubtitleIndex, setActiveSubtitleIndex] = useState<number | null>(
    () => plan.selected_tracks.subtitle?.index ?? null,
  );
  const lastSubtitleIndexRef = useRef<number | null>(null);
  const subtitleSelectionWasManualRef = useRef(false);
  // Per-session subtitle delay in ms. Positive = show later. Reset when the
  // underlying file changes so sync adjustments don't carry across media.
  const [subtitleDelayMs, setSubtitleDelayMs] = useState(0);
  useEffect(() => {
    setSubtitleDelayMs(0);
  }, [activeFileId]);

  // -- Live AI subtitle translation (streamed over the realtime websocket) --
  // While a translation runs, a synthetic "live" track is added to the list and
  // selected; cues arrive over the websocket and the player pauses until the
  // region near the playhead is covered, then resumes.
  const [liveTranslation, setLiveTranslation] = useState<{
    trackKey: string;
    language: string;
    label: string;
  } | null>(null);
  const [liveCues, setLiveCues] = useState<ParsedCue[]>([]);
  const [pendingTranslationHandoff, setPendingTranslationHandoff] = useState<{
    language: string;
    planRevision: number;
    existingTrackIndexes: number[];
  } | null>(null);
  const [translationBuffering, setTranslationBuffering] = useState(false);
  const translationPauseRef = useRef(false);
  const translationResumeTimerRef = useRef<number | null>(null);
  // Whether playback should auto-resume once buffering ends. Captured at
  // translation start: if the viewer had deliberately paused, we don't yank
  // them back into playback.
  const translationResumeOnFinishRef = useRef(false);
  // The subtitle selection active before a translation hijacked it, so a failed
  // translation can restore it instead of leaving subtitles off.
  const preTranslationSubtitleIndexRef = useRef<number | null>(null);

  // Drop any live translation when the media changes so a stale track from the
  // previous file never lingers.
  useEffect(() => {
    // Disarm any pending resume timeout so a translation from the previous file
    // can't fire its 30s callback against the new playback state.
    if (translationResumeTimerRef.current !== null) {
      window.clearTimeout(translationResumeTimerRef.current);
      translationResumeTimerRef.current = null;
    }
    setPendingTranslationHandoff(null);
    setLiveTranslation(null);
    setLiveCues([]);
    setTranslationBuffering(false);
    translationPauseRef.current = false;
  }, [activeFileId]);

  // Merge the live track into the track list the player + menu see.
  const effectiveSubtitleTracks = useMemo(() => {
    if (!liveTranslation) return subtitleUrls;
    return [
      ...subtitleUrls,
      {
        index: LIVE_SUBTITLE_INDEX,
        language: liveTranslation.language,
        label: liveTranslation.label || "AI translation",
        source: "downloaded" as const,
        codec: "srt",
        url: "",
        live: true,
      },
    ];
  }, [subtitleUrls, liveTranslation]);

  // -- Plan-derived transport --
  // The plan names its own protocol and timeline; nothing here is inferred from
  // codec strings or from what the engine reports.
  const isHlsStream = plan.stream.protocol === "hls";
  const effectiveStreamUrl = streamUrl;
  const isPlayerReady = effectiveStreamUrl !== "";
  const reportCurrentPlanFailure = useCallback(
    (failure: FailureV3): boolean => {
      if (!onPlanFailure) return false;
      const failureKey = `${sessionId}:${plan.plan_attempt_key}`;
      if (reportedPlanFailureKeyRef.current === failureKey) return true;
      reportedPlanFailureKeyRef.current = failureKey;
      transportFailedForPlanRevisionRef.current = planRevision;
      setError(null);
      onPlanFailure(failure, currentTimeRef.current);
      return true;
    },
    [onPlanFailure, plan.plan_attempt_key, planRevision, sessionId],
  );

  useEffect(() => {
    transportFailedForPlanRevisionRef.current = null;
  }, [planRevision]);

  const failHlsStartup = useCallback(() => {
    console.error("[hls.js] Playback startup timed out or exhausted recovery attempts");

    const activeHls = hlsRef.current;
    hlsRef.current = null;
    activeHls?.destroy();

    const video = videoRef.current;
    if (video) {
      video.removeAttribute("src");
      video.load();
    }
    if (
      !reportCurrentPlanFailure({
        classification: "startup_timeout",
        message: "HLS playback exhausted its startup recovery budget.",
      })
    ) {
      setError("Playback failed. The media could not be loaded.");
    }
  }, [reportCurrentPlanFailure]);

  useEffect(() => {
    if (!isHlsStream || !isPlayerReady) return;

    const guard = new HlsStartupGuard(failHlsStartup);
    hlsStartupGuardRef.current = guard;

    return () => {
      guard.dispose();
      if (hlsStartupGuardRef.current === guard) {
        hlsStartupGuardRef.current = null;
      }
    };
  }, [failHlsStartup, isHlsStream, isPlayerReady, planRevision]);

  // The media's full runtime, from the plan and nowhere else: on an HLS copy
  // remux the engine reports only the length produced so far, so substituting it
  // would make the scrubber grow while the viewer watches.
  const backendDuration = plan.source.duration_seconds ?? propDuration ?? 0;
  backendDurationRef.current = backendDuration;
  const effectiveInitialPosition = plan.timeline.player_start_seconds;
  const canSeekAnywhere = plan.timeline.can_seek_anywhere;
  // The menu is the plan's; which entry is lit is the session's own preference,
  // since `auto` is a valid preference that names no rung.
  const activeQualityId = qualityPreference;
  const qualityOptions = useMemo(() => qualityOptionsFromPlanV3(plan), [plan]);

  // The file the server actually planned against, which is not necessarily the
  // one that was asked for — a fallback to an alternate version shows up here.
  const effectiveVersion = useMemo(
    () => versions.find((v) => v.file_id === plan.effective_media_file_id) ?? selectedVersion,
    [plan.effective_media_file_id, selectedVersion, versions],
  );

  // Any stream restart (transcode restart on seek, quality/audio switch,
  // turning off bitmap burn-in) reloads the <video> element, which can orphan
  // a programmatic TextTrack — cuechange stops firing and the last cue
  // freezes on screen. Bump a generation on every settled stream change so
  // useSubtitleTracks rebuilds its track against the new element; the rebuild
  // carries loaded cues and window coverage over, so it costs no refetch.
  const [subtitleStreamGeneration, setSubtitleStreamGeneration] = useState(0);
  const lastSubtitlePlanRevisionRef = useRef<number | null>(null);
  useEffect(() => {
    if (!isPlayerReady) return;
    const changed =
      lastSubtitlePlanRevisionRef.current !== null &&
      lastSubtitlePlanRevisionRef.current !== planRevision;
    lastSubtitlePlanRevisionRef.current = planRevision;
    if (changed) {
      setSubtitleStreamGeneration((generation) => generation + 1);
    }
  }, [isPlayerReady, planRevision]);

  const isFirefoxBrowser =
    typeof navigator !== "undefined" &&
    /firefox/i.test(navigator.userAgent) &&
    !/seamonkey/i.test(navigator.userAgent);
  const watchTogether =
    watchTogetherConnection ??
    ({
      connectionState: "disconnected",
      room: null,
      suggestions: [],
      closedReason: null,
      transportCommand: null,
      serverTimeOffsetMs: 0,
      sendRoomMessage: () => ({ ok: false }),
      updatePolicy: async () => null,
      selectItem: async () => null,
      closeRoom: async () => {},
      createSuggestion: async () => {},
      deleteSuggestion: async () => {},
      vote: async () => {},
      unvote: async () => {},
      promoteSuggestion: async () => null,
    } satisfies WatchTogetherRoomConnectionResult);
  const watchTogetherSync = useWatchTogetherPlaybackSync({
    roomConnection: watchTogether,
    sessionId,
    videoRef,
    streamOriginRef: timelineOffsetRef,
  });
  const roomPlaybackActive = !!watchTogetherRoomId && !watchTogether.closedReason;
  const roomSyncWaiting = watchTogether.room?.playback_state === "waiting";
  const watchTogetherRoomActive = watchTogether.room !== null;

  const showWatchTogetherNotice = useCallback((message: string, tone: "info" | "warning") => {
    setNotice({
      title: "Watch Party",
      message,
      tone,
    });
  }, []);

  const resetLeaveState = useCallback(() => {
    leaveInProgressRef.current = false;
    if (isMountedRef.current) {
      setIsLeaving(false);
    }
  }, []);

  useEffect(() => {
    return () => {
      isMountedRef.current = false;
      if (roomCommandTimerRef.current !== null) {
        window.clearTimeout(roomCommandTimerRef.current);
      }
    };
  }, []);

  useEffect(() => {
    if (displayMode === "foreground") {
      resetLeaveState();
    }
  }, [displayMode, resetLeaveState, sessionId]);

  // A codec-copy remux delivered over HLS. Firefox is the only engine that
  // stalls on these, and the plan names the route outright.
  const isCopyOriginalHLS = plan.delivery === "server_remux_hls";

  // Keep the player-local clock mapped onto the canonical media timeline.
  const timelineOffsetSeconds = plan.timeline.timeline_offset_seconds;
  timelineOffsetRef.current = timelineOffsetSeconds;

  // Media-time position playback is heading to, for consumers that need a
  // position before the element has media loaded (when currentTime still
  // reads 0): an in-flight seek target, else the session's start position.
  subtitleFetchAnchorRef.current =
    pendingSeekTime ?? toMediaTime(effectiveInitialPosition, timelineOffsetSeconds);

  useEffect(() => {
    if (backendDuration > 0) {
      setDuration(backendDuration);
    }
  }, [backendDuration]);

  useEffect(() => {
    currentTimeRef.current = currentTime;
  }, [currentTime]);

  useEffect(() => {
    durationRef.current = duration;
  }, [duration]);

  useEffect(() => {
    setNotice(null);
  }, [sessionId]);

  useEffect(() => {
    if (!watchTogetherRoomId || watchTogether.closedReason) {
      return;
    }
    if (watchTogether.connectionState === "connected") {
      return;
    }

    showWatchTogetherNotice(
      "Reconnecting to room. Controls are temporarily unavailable.",
      "warning",
    );
  }, [
    showWatchTogetherNotice,
    watchTogether.closedReason,
    watchTogether.connectionState,
    watchTogetherRoomId,
  ]);

  useEffect(() => {
    compatibilityFallbackKeyRef.current = null;
  }, [sessionId]);

  // Warm the connection to the stream origin (a proxy node in distributed
  // deployments) while the transcode start request is still in flight, so
  // the first manifest fetch doesn't pay DNS/TCP/TLS handshakes.
  useEffect(() => {
    if (streamUrl) preconnectToStreamOrigin(streamUrl);
  }, [streamUrl]);

  useEffect(() => {
    setPendingSeekTime(null);
  }, [planRevision]);

  // Firefox stalls on codec-copy remuxes it nominally accepts. Both fallbacks
  // report an honest classification and let the server pick the next route —
  // the client no longer names a "compatibility" rung of its own.
  useEffect(() => {
    if (
      !isFirefoxBrowser ||
      !isCopyOriginalHLS ||
      !isPlayerReady ||
      replanning ||
      !awaitingFirstFrame ||
      error
    ) {
      return;
    }

    const fallbackKey = `${sessionId}:${plan.plan_attempt_key}`;
    if (compatibilityFallbackKeyRef.current === fallbackKey) {
      return;
    }

    const timer = setTimeout(() => {
      if (compatibilityFallbackKeyRef.current === fallbackKey) {
        return;
      }
      compatibilityFallbackKeyRef.current = fallbackKey;
      setNotice({
        title: "Compatibility mode",
        message: "Firefox stalled on the original stream. Retrying with encoded video.",
        tone: "info",
      });
      reportCurrentPlanFailure({
        classification: "startup_timeout",
        message: "Firefox produced no frames from the copy remux before the startup deadline.",
      });
    }, FIREFOX_COMPATIBILITY_FALLBACK_DELAY_MS);

    return () => clearTimeout(timer);
  }, [
    awaitingFirstFrame,
    error,
    isCopyOriginalHLS,
    isFirefoxBrowser,
    isPlayerReady,
    reportCurrentPlanFailure,
    plan.plan_attempt_key,
    replanning,
    sessionId,
  ]);

  useEffect(() => {
    if (!isFirefoxBrowser || !error || !isCopyOriginalHLS || !isPlayerReady || replanning) {
      return;
    }

    const fallbackKey = `${sessionId}:${plan.plan_attempt_key}`;
    if (compatibilityFallbackKeyRef.current === fallbackKey) {
      return;
    }

    compatibilityFallbackKeyRef.current = fallbackKey;
    setError(null);
    setNotice({
      title: "Compatibility mode",
      message: "Firefox rejected the original stream. Retrying with encoded video.",
      tone: "warning",
    });
    reportCurrentPlanFailure({
      classification: "decoder_error",
      message: "Firefox rejected the copy remux.",
    });
  }, [
    error,
    isCopyOriginalHLS,
    isFirefoxBrowser,
    isPlayerReady,
    reportCurrentPlanFailure,
    plan.plan_attempt_key,
    replanning,
    sessionId,
  ]);

  // A failed recovery leaves no transport behind. Surface the refusal for the
  // same plan revision and re-arm its failure key so a later media error can
  // retry a transiently failed recovery request.
  useEffect(() => {
    if (!replanError || replanning) return;

    const failureKey = `${sessionId}:${plan.plan_attempt_key}`;
    if (reportedPlanFailureKeyRef.current === failureKey) {
      reportedPlanFailureKeyRef.current = null;
    }
    if (transportFailedForPlanRevisionRef.current === planRevision || !isPlayerReady) {
      setError(replanError);
    }
  }, [isPlayerReady, plan.plan_attempt_key, planRevision, replanError, replanning, sessionId]);

  // -- Remux seeking (callback-based) --
  // Only the progressive/direct routes take this path; HLS seeking is handled
  // against the plan's timeline below.
  const { handleSeek } = useRemuxSeeking(videoRef);

  const performPlayerSeek = useCallback(
    (seconds: number) => {
      const video = videoRef.current;
      if (!video) return;

      setPendingSeekTime(seconds);
      setCurrentTime(seconds);

      const nativeSeconds = toPlayerTime(seconds, timelineOffsetRef.current);
      if (canSeekAnywhere) {
        if (isHlsStream) video.currentTime = nativeSeconds;
        else handleSeek(nativeSeconds);
        return;
      }

      const seekable = video.seekable;
      for (let i = 0; i < seekable.length; i++) {
        if (nativeSeconds >= seekable.start(i) && nativeSeconds <= seekable.end(i)) {
          if (isHlsStream) video.currentTime = nativeSeconds;
          else handleSeek(nativeSeconds);
          return;
        }
      }

      // Outside the server-anchored window: this is a timeline operation, not
      // a failure, so it asks for a reanchor rather than reporting a failure.
      onReanchorSeek?.(seconds);
    },
    [canSeekAnywhere, handleSeek, isHlsStream, onReanchorSeek],
  );

  const handlePlayerSeek = useCallback(
    (seconds: number) => {
      if (
        watchTogetherRoomId &&
        !watchTogether.closedReason &&
        (watchTogether.connectionState !== "connected" || !watchTogether.room)
      ) {
        showWatchTogetherNotice(
          "Reconnecting to room. Controls are temporarily unavailable.",
          "warning",
        );
        return;
      }
      if (watchTogether.room && !watchTogether.room.self_can_manage_room) {
        showWatchTogetherNotice("Only the host can seek the room.", "warning");
        return;
      }
      if (watchTogether.room && watchTogetherSync.attachedSessionId !== sessionId) {
        showWatchTogetherNotice("Joining room playback. Try again in a moment.", "info");
        return;
      }

      if (watchTogether.room) {
        const video = videoRef.current;
        watchTogetherSync.requestTransport("seek", seconds, video?.paused ?? true);
        return;
      }
      performPlayerSeek(seconds);
    },
    [
      performPlayerSeek,
      sessionId,
      showWatchTogetherNotice,
      watchTogether,
      watchTogetherRoomId,
      watchTogetherSync,
    ],
  );

  useEffect(() => {
    performPlayerSeekRef.current = performPlayerSeek;
  }, [performPlayerSeek]);

  // -- Keyboard seek adapter --
  // Keyboard shortcuts read player-local video.currentTime (e.g., 10) and add
  // ±10 s. This wrapper remaps that local time back onto the media timeline
  // before dispatching the seek request.
  const handleKeyboardSeek = useCallback(
    (seconds: number) => {
      handlePlayerSeek(toMediaTime(seconds, timelineOffsetRef.current));
    },
    [handlePlayerSeek],
  );

  // -- Watch progress reporting --
  const flushWatchProgress = useWatchProgress(sessionId, videoRef, timelineOffsetRef);

  const buildExitState = useCallback((): PlaybackExitState => {
    const video = videoRef.current;
    const positionSeconds = toMediaTime(
      video?.currentTime ?? currentTime,
      timelineOffsetRef.current,
    );
    // positionSeconds is media time, so the runtime paired with it must be too.
    const durationSeconds = mediaDurationSeconds(backendDurationRef.current, duration);

    return {
      positionSeconds,
      durationSeconds,
      lastFileId: activeFileId ?? selectedVersion?.file_id,
      lastResolution: selectedVersion?.resolution,
      lastHDR: selectedVersion?.hdr,
      lastCodecVideo: selectedVersion?.codec_video,
      lastEditionKey: selectedVersion?.edition_key,
    };
  }, [activeFileId, currentTime, duration, selectedVersion]);

  useEffect(() => {
    if (!watchTogetherRoomId || !watchTogether.closedReason || leaveInProgressRef.current) {
      return;
    }

    leaveInProgressRef.current = true;
    setIsLeaving(true);

    const exitState = buildExitState();
    let cancelled = false;

    const exitPlayback = async () => {
      try {
        await Promise.race([
          flushWatchProgress(),
          new Promise<void>((resolve) => {
            window.setTimeout(resolve, EXIT_PROGRESS_FLUSH_TIMEOUT_MS);
          }),
        ]);
      } catch {
        // Best effort — cleanup still sends a keepalive progress update on unmount.
      }

      try {
        await onExit({
          ...exitState,
          destinationHref: "/rooms/join",
        });
      } finally {
        if (!cancelled) {
          resetLeaveState();
        }
      }
    };

    void exitPlayback();

    return () => {
      cancelled = true;
    };
  }, [
    buildExitState,
    flushWatchProgress,
    onExit,
    resetLeaveState,
    watchTogether.closedReason,
    watchTogetherRoomId,
  ]);

  const handleLeave = useCallback(
    async (action: "exit" | "minimize") => {
      if (leaveInProgressRef.current) return;

      leaveInProgressRef.current = true;
      setIsLeaving(true);

      const exitState = buildExitState();

      try {
        await Promise.race([
          flushWatchProgress(),
          new Promise<void>((resolve) => {
            window.setTimeout(resolve, EXIT_PROGRESS_FLUSH_TIMEOUT_MS);
          }),
        ]);
      } catch {
        // Best effort — cleanup still sends a keepalive progress update on unmount.
      }

      try {
        if (
          action === "exit" &&
          watchTogetherRoomId &&
          !watchTogether.closedReason &&
          watchTogether.room?.self_can_manage_room
        ) {
          await watchTogether.closeRoom();
          await onExit({
            ...exitState,
            destinationHref: "/rooms/join",
          });
          return;
        }

        if (action === "minimize" && onMinimize) {
          await onMinimize(exitState);
          return;
        }

        await onExit(exitState);
      } finally {
        if (action === "minimize") {
          resetLeaveState();
        }
      }
    },
    [
      buildExitState,
      flushWatchProgress,
      onExit,
      onMinimize,
      resetLeaveState,
      watchTogether,
      watchTogetherRoomId,
    ],
  );

  const handleExit = useCallback(async () => {
    await handleLeave("exit");
  }, [handleLeave]);

  const handleMinimize = useCallback(async () => {
    await handleLeave("minimize");
  }, [handleLeave]);

  // -- Subtitle toggle callback --
  const toggleCaptions = useCallback(() => {
    subtitleSelectionWasManualRef.current = true;
    if (activeSubtitleIndex !== null) {
      lastSubtitleIndexRef.current = activeSubtitleIndex;
      setActiveSubtitleIndex(null);
      onSubtitleChanged?.(null);
    } else {
      const restoredIndex = lastSubtitleIndexRef.current;
      setActiveSubtitleIndex(restoredIndex);
      onSubtitleChanged?.(restoredIndex);
    }
  }, [activeSubtitleIndex, onSubtitleChanged]);

  const handleSubtitleSelect = useCallback(
    (index: number | null, inventoryTrack?: SubtitleInventoryItemV3) => {
      subtitleSelectionWasManualRef.current = true;
      setActiveSubtitleIndex(index);
      // The in-progress live translation track is synthetic (a sentinel index
      // that exists only in memory); never persist it as the saved preference or
      // we'd store a nonexistent track and lose the real selection.
      if (index === LIVE_SUBTITLE_INDEX) return;
      onSubtitleChanged?.(index, inventoryTrack);
    },
    [onSubtitleChanged],
  );

  useEffect(() => {
    if (!pendingTranslationHandoff || replanning) return;

    const refreshSettled =
      planRevision !== pendingTranslationHandoff.planRevision || replanError !== null;
    if (!refreshSettled) return;

    const normalizedLanguage = pendingTranslationHandoff.language.trim().toLowerCase();
    const track = subtitleUrls.find(
      (candidate) =>
        candidate.source === "downloaded" &&
        candidate.language.trim().toLowerCase() === normalizedLanguage &&
        !pendingTranslationHandoff.existingTrackIndexes.includes(candidate.index),
    );
    setPendingTranslationHandoff(null);
    if (!track) return;

    handleSubtitleSelect(track.index);
    setLiveTranslation(null);
    setLiveCues([]);
  }, [
    handleSubtitleSelect,
    pendingTranslationHandoff,
    planRevision,
    replanError,
    replanning,
    subtitleUrls,
  ]);

  // The media-time playhead, sent with a translate request so the server starts
  // where the viewer is watching.
  const getSubtitleStartPosition = useCallback(() => {
    return subtitleStartPositionSeconds(
      videoRef.current?.readyState ?? 0,
      currentTimeRef.current,
      subtitleFetchAnchorRef.current,
    );
  }, []);

  const resumeFromTranslationPause = useCallback(() => {
    if (translationResumeTimerRef.current !== null) {
      window.clearTimeout(translationResumeTimerRef.current);
      translationResumeTimerRef.current = null;
    }
    if (translationPauseRef.current) {
      translationPauseRef.current = false;
      // Only resume if the viewer was playing when the translation began; if
      // they had paused on purpose, leave them paused.
      if (translationResumeOnFinishRef.current) {
        void videoRef.current?.play().catch(() => {});
      }
    }
    setTranslationBuffering(false);
  }, []);

  // Intercept live-translation events; forward everything else to the parent.
  const handleRealtimeEvent = useCallback(
    (event: PlaybackRealtimeEventEnvelope) => {
      switch (event.name) {
        case "subtitle_ready": {
          // Broadcast to every viewer of the file when a generated track is
          // persisted. The payload carries the server-assigned ordinal, so the
          // track is folded in at that ordinal without a round trip; only a
          // payload the server could not resolve falls back to a replan.
          if (event.payload.file_id === activeFileId) {
            if (event.payload.track) {
              onApplySubtitleTrack?.(event.payload.track);
            } else {
              onRefreshSubtitles?.(getSubtitleStartPosition());
            }
          }
          break;
        }
        case "subtitle_translation_started": {
          // Remember the real selection we're displacing and whether we were
          // playing, so completion/failure can restore the right state.
          const wasPlaying = !(videoRef.current?.paused ?? true);
          translationResumeOnFinishRef.current = wasPlaying;
          setActiveSubtitleIndex((idx) => {
            if (idx !== LIVE_SUBTITLE_INDEX) {
              preTranslationSubtitleIndexRef.current = idx;
            }
            return LIVE_SUBTITLE_INDEX;
          });
          setLiveCues([]);
          setLiveTranslation({
            trackKey: event.payload.track_key,
            language: event.payload.language,
            label: event.payload.label ?? "",
          });
          subtitleSelectionWasManualRef.current = true;
          translationPauseRef.current = true;
          setTranslationBuffering(true);
          // Only pause if the viewer was playing; don't disturb a deliberate pause.
          if (wasPlaying) videoRef.current?.pause();
          if (translationResumeTimerRef.current !== null) {
            window.clearTimeout(translationResumeTimerRef.current);
          }
          translationResumeTimerRef.current = window.setTimeout(
            resumeFromTranslationPause,
            TRANSLATION_RESUME_TIMEOUT_MS,
          );
          break;
        }
        case "subtitle_translation_cues": {
          const cues = event.payload.cues.map((c) => ({
            start: c.start,
            end: c.end,
            text: c.text,
          }));
          setLiveCues((prev) => [...prev, ...cues]);
          break;
        }
        case "subtitle_translation_completed": {
          resumeFromTranslationPause();
          // Hand off from the ephemeral live track to the persisted downloaded
          // one. The payload names the ordinal the server assigned it, so the
          // handoff is a fold-in plus a select — no ordinal is derived here.
          // Without an entry the live track (which already holds the full cue
          // set) stays on screen while a replan re-reads the inventory.
          if (event.payload.track) {
            const track = event.payload.track;
            onApplySubtitleTrack?.(track);
            setLiveTranslation(null);
            setLiveCues([]);
            handleSubtitleSelect(track.combined_index, track);
          } else {
            const language = event.payload.language.trim().toLowerCase();
            setPendingTranslationHandoff({
              language: event.payload.language,
              planRevision,
              existingTrackIndexes: subtitleUrls
                .filter(
                  (track) =>
                    track.source === "downloaded" &&
                    track.language.trim().toLowerCase() === language,
                )
                .map((track) => track.index),
            });
            onRefreshSubtitles?.(getSubtitleStartPosition());
          }
          break;
        }
        case "subtitle_translation_failed": {
          resumeFromTranslationPause();
          setLiveTranslation(null);
          setLiveCues([]);
          // Restore the selection the translation displaced rather than leaving
          // subtitles off.
          const restore = preTranslationSubtitleIndexRef.current;
          setActiveSubtitleIndex((idx) => (idx === LIVE_SUBTITLE_INDEX ? restore : idx));
          toast.error(
            event.payload.message
              ? `Translation failed: ${event.payload.message}`
              : "Subtitle translation failed",
          );
          break;
        }
        default:
          onRealtimeEvent?.(event);
      }
    },
    [
      activeFileId,
      getSubtitleStartPosition,
      handleSubtitleSelect,
      onApplySubtitleTrack,
      onRealtimeEvent,
      onRefreshSubtitles,
      planRevision,
      resumeFromTranslationPause,
      subtitleUrls,
    ],
  );

  // Resume as soon as the first translated cues arrive. Playhead-first
  // translation means the cues covering the current position are delivered
  // first, so the first batch is enough; and when the playhead is past the last
  // cue (e.g. end credits) there is nothing at the playhead to wait for, so we
  // still resume here rather than stalling until the 30s timeout.
  useEffect(() => {
    if (!translationPauseRef.current || liveCues.length === 0) return;
    resumeFromTranslationPause();
  }, [liveCues, resumeFromTranslationPause]);

  useEffect(
    () => () => {
      if (translationResumeTimerRef.current !== null) {
        window.clearTimeout(translationResumeTimerRef.current);
      }
    },
    [],
  );

  // -- PiP toggle --
  const handleTogglePiP = useCallback(async () => {
    const video = videoRef.current;
    if (!video) return;
    if (document.pictureInPictureElement) {
      await document.exitPictureInPicture();
    } else {
      await video.requestPictureInPicture();
    }
  }, []);

  useEffect(() => {
    autoEnterPictureInPictureAttemptedRef.current = false;
  }, [sessionId]);

  useEffect(() => {
    endedFiredRef.current = false;
    setHasEnded(false);
  }, [sessionId]);

  useEffect(() => {
    onEndedRef.current = onEnded;
  }, [onEnded]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video || !onPictureInPictureChange) return;

    const handleEnterPictureInPicture = () =>
      onPictureInPictureChange({
        active: true,
        playbackContinues: !video.paused,
      });
    const handleLeavePictureInPicture = () => {
      window.setTimeout(() => {
        onPictureInPictureChange({
          active: false,
          playbackContinues: !video.paused,
        });
      }, 0);
    };

    video.addEventListener("enterpictureinpicture", handleEnterPictureInPicture);
    video.addEventListener("leavepictureinpicture", handleLeavePictureInPicture);

    return () => {
      video.removeEventListener("enterpictureinpicture", handleEnterPictureInPicture);
      video.removeEventListener("leavepictureinpicture", handleLeavePictureInPicture);
    };
  }, [onPictureInPictureChange]);

  useEffect(() => {
    if (!autoEnterPictureInPicture || displayMode !== "detached") {
      return;
    }

    const video = videoRef.current;
    if (!video || !isPlayerReady || autoEnterPictureInPictureAttemptedRef.current) {
      return;
    }

    autoEnterPictureInPictureAttemptedRef.current = true;
    const transferPictureInPicture = async () => {
      try {
        const currentPictureInPictureElement = document.pictureInPictureElement;
        if (currentPictureInPictureElement === video) {
          return;
        }
        if (currentPictureInPictureElement) {
          await document.exitPictureInPicture();
        }
        await video.requestPictureInPicture();
      } catch {
        autoEnterPictureInPictureAttemptedRef.current = false;
      }
    };

    void transferPictureInPicture();
  }, [autoEnterPictureInPicture, displayMode, isPlayerReady, sessionId]);

  // -- Next episode auto-play --
  const handleNavigate = useCallback(
    (contentId: string) => {
      onNavigateEpisode?.(contentId);
    },
    [onNavigateEpisode],
  );

  const nextEpisode = useNextEpisode(
    roomPlaybackActive ? null : autoPlayNextPreview && preview ? preview : credits,
    roomPlaybackActive ? undefined : seriesContext,
    currentTime,
    handleNavigate,
  );

  // Previous-episode lookup (mirrors the helper in useNextEpisode). Auto-play
  // is next-only, so we just need the reference + a navigation callback for
  // the floating player cluster.
  const prevEpisodeRef = (() => {
    if (!seriesContext) return null;
    const idx = seriesContext.episodes.findIndex(
      (ep) =>
        ep.seasonNumber === seriesContext.currentSeason &&
        ep.episodeNumber === seriesContext.currentEpisode,
    );
    if (idx <= 0) return null;
    return seriesContext.episodes[idx - 1] ?? null;
  })();
  const goToPrevEpisode = useCallback(() => {
    if (prevEpisodeRef) handleNavigate(prevEpisodeRef.contentId);
  }, [prevEpisodeRef, handleNavigate]);

  // Title strip copy passed into the floating HUD.
  const hudTitle = seriesContext?.seriesTitle ?? title;
  const hudSubtitle = seriesContext
    ? `S${seriesContext.currentSeason} · E${seriesContext.currentEpisode}${title ? ` — ${title}` : ""}`
    : year
      ? String(year)
      : undefined;
  const cancelNextEpisodeAutoPlay = nextEpisode.cancelAutoPlay;
  const cancelNextEpisodeAutoPlayRef = useRef(cancelNextEpisodeAutoPlay);
  const flushWatchProgressRef = useRef(flushWatchProgress);

  useEffect(() => {
    cancelNextEpisodeAutoPlayRef.current = cancelNextEpisodeAutoPlay;
  }, [cancelNextEpisodeAutoPlay]);

  useEffect(() => {
    flushWatchProgressRef.current = flushWatchProgress;
  }, [flushWatchProgress]);

  // Cancel the in-player credits countdown when entering postroll mode,
  // since PlayingNextScreen takes over next-episode navigation.
  useEffect(() => {
    if (displayMode === "postroll") {
      cancelNextEpisodeAutoPlay();
    }
  }, [cancelNextEpisodeAutoPlay, displayMode]);

  // -- Intro skip --
  const showIntroSkip = intro != null && currentTime >= intro.start && currentTime < intro.end;
  const showRecapSkip = recap != null && currentTime >= recap.start && currentTime < recap.end;

  const skipIntro = useCallback(() => {
    if (intro) handlePlayerSeek(intro.end);
  }, [intro, handlePlayerSeek]);

  const skipRecap = useCallback(() => {
    if (recap) handlePlayerSeek(recap.end);
  }, [recap, handlePlayerSeek]);

  useEffect(() => {
    if (!autoSkipIntro || !intro || !isPlayerReady || awaitingFirstFrame) {
      return;
    }
    if (currentTime < intro.start || currentTime >= intro.end) {
      return;
    }
    if (
      roomPlaybackActive &&
      (!watchTogether.room?.self_can_manage_room ||
        watchTogetherSync.attachedSessionId !== sessionId)
    ) {
      return;
    }

    const introKey = `${sessionId}:${activeFileId ?? "unknown"}:${intro.start}:${intro.end}`;
    if (autoSkippedIntroKeyRef.current === introKey) {
      return;
    }
    autoSkippedIntroKeyRef.current = introKey;
    handlePlayerSeek(intro.end);
  }, [
    activeFileId,
    autoSkipIntro,
    awaitingFirstFrame,
    currentTime,
    handlePlayerSeek,
    intro,
    isPlayerReady,
    roomPlaybackActive,
    sessionId,
    watchTogether.room?.self_can_manage_room,
    watchTogetherSync.attachedSessionId,
  ]);

  useEffect(() => {
    if (!autoSkipRecap || !recap || !isPlayerReady || awaitingFirstFrame) {
      return;
    }
    if (currentTime < recap.start || currentTime >= recap.end) {
      return;
    }
    if (
      roomPlaybackActive &&
      (!watchTogether.room?.self_can_manage_room ||
        watchTogetherSync.attachedSessionId !== sessionId)
    ) {
      return;
    }

    const recapKey = `${sessionId}:${activeFileId ?? "unknown"}:${recap.start}:${recap.end}`;
    if (autoSkippedRecapKeyRef.current === recapKey) {
      return;
    }
    autoSkippedRecapKeyRef.current = recapKey;
    handlePlayerSeek(recap.end);
  }, [
    activeFileId,
    autoSkipRecap,
    awaitingFirstFrame,
    currentTime,
    handlePlayerSeek,
    isPlayerReady,
    recap,
    roomPlaybackActive,
    sessionId,
    watchTogether.room?.self_can_manage_room,
    watchTogetherSync.attachedSessionId,
  ]);

  // Only the bitrate matters for buffer sizing, and the plan states what is
  // actually being delivered rather than what the source file happens to hold.
  const plannedBitrateKbps = plan.effective_recipe.bitrate_kbps ?? 0;

  // -- hls.js lifecycle --
  useEffect(() => {
    const video = videoRef.current;
    if (!video || !isPlayerReady || hlsStartupGuardRef.current?.hasFailed()) return;

    let hls: HlsType | null = null;
    let destroyed = false;
    let autoplayStarted = false;
    let nativeHLSMetadataHandler: (() => void) | null = null;

    mediaRecoveryAttemptsRef.current = 0;
    setError(null);
    setAwaitingFirstFrame(true);

    const cleanupStartupListeners = () => {
      video.removeEventListener("loadeddata", attemptAutoplayWhenReady);
      video.removeEventListener("canplay", attemptAutoplayWhenReady);
      video.removeEventListener("loadedmetadata", attemptAutoplayWhenReady);
      if (nativeHLSMetadataHandler) {
        video.removeEventListener("loadedmetadata", nativeHLSMetadataHandler);
        nativeHLSMetadataHandler = null;
      }
    };

    const attemptAutoplayWhenReady = () => {
      if (destroyed || autoplayStarted || hlsStartupGuardRef.current?.hasFailed()) return;
      // HAVE_FUTURE_DATA means the browser has enough media to advance beyond
      // the current frame. Starting earlier can produce a visible first-frame
      // freeze where audio advances before video begins moving.
      if (video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA) return;
      autoplayStarted = true;
      cleanupStartupListeners();
      if (!shouldAutoPlay) {
        hlsStartupGuardRef.current?.markPlaybackStarted();
        setAwaitingFirstFrame(false);
        setPlaying(false);
        return;
      }
      video.play().catch(() => setPlaying(false));
    };

    video.addEventListener("loadeddata", attemptAutoplayWhenReady);
    video.addEventListener("canplay", attemptAutoplayWhenReady);

    async function init() {
      if (!video || destroyed) return;

      if (isHlsStream) {
        try {
          const Hls = await hlsPromise;
          if (destroyed || hlsStartupGuardRef.current?.hasFailed()) return;

          if (Hls.isSupported()) {
            const maxBufferLength = plannedBitrateKbps >= 25000 ? 60 : 120;
            const retryingLoadPolicy = {
              maxTimeToFirstByteMs: 45000,
              maxLoadTimeMs: 45000,
              timeoutRetry: { maxNumRetry: 3, retryDelayMs: 500, maxRetryDelayMs: 3000 },
              errorRetry: { maxNumRetry: 3, retryDelayMs: 500, maxRetryDelayMs: 3000 },
            };

            hls = new Hls({
              lowLatencyMode: false,
              backBufferLength: Infinity,
              maxBufferLength,
              maxMaxBufferLength: maxBufferLength,
              startPosition: effectiveInitialPosition,
              startFragPrefetch: true,
              // Segment requests may block while FFmpeg encodes on demand.
              // Remote transcode nodes can also briefly defer the initial
              // manifest until enough data is available for playback.
              manifestLoadPolicy: { default: retryingLoadPolicy },
              playlistLoadPolicy: { default: retryingLoadPolicy },
              fragLoadPolicy: {
                default: retryingLoadPolicy,
              },
            });

            hls.on(Hls.Events.ERROR, (_event, data) => {
              if (!data.fatal || destroyed) return;

              console.error("[hls.js] Fatal error:", {
                type: data.type,
                details: data.details,
                reason: data.reason,
                url: data.frag?.url ?? data.url,
                error: data.error?.message,
              });

              const now = Date.now();
              if (now - lastRecoveryRef.current < 3000) return;
              lastRecoveryRef.current = now;

              if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
                if (hlsStartupGuardRef.current?.handleFatalNetworkError() ?? true) {
                  console.warn("[hls.js] Fatal network error, attempting recovery...");
                  hls?.startLoad();
                } else {
                  console.error("[hls.js] Fatal startup network error, giving up");
                }
              } else if (data.type === Hls.ErrorTypes.MEDIA_ERROR) {
                if (mediaRecoveryAttemptsRef.current === 0) {
                  console.warn("[hls.js] Fatal media error, attempting recovery...");
                  hls?.recoverMediaError();
                } else if (mediaRecoveryAttemptsRef.current === 1) {
                  console.warn("[hls.js] Fatal media error (2nd), swapping audio codec...");
                  hls?.swapAudioCodec();
                  hls?.recoverMediaError();
                } else {
                  console.error("[hls.js] Fatal media error, giving up after 3 attempts");
                  if (
                    !reportCurrentPlanFailure({
                      classification: "decoder_error",
                      message: "HLS media recovery failed after three attempts.",
                    })
                  ) {
                    setError("Playback failed. Please try again.");
                  }
                  hls?.destroy();
                  hlsRef.current = null;
                }
                mediaRecoveryAttemptsRef.current++;
              } else {
                console.error("[hls.js] Unrecoverable error:", data);
                if (
                  !reportCurrentPlanFailure({
                    classification: "decoder_error",
                    message: `HLS reported an unrecoverable ${data.type} error.`,
                  })
                ) {
                  setError("Playback failed. Please try again.");
                }
                hls?.destroy();
                hlsRef.current = null;
              }
            });

            hls.on(Hls.Events.MANIFEST_PARSED, () => {
              if (destroyed) return;
              attemptAutoplayWhenReady();
            });

            hls.on(Hls.Events.BUFFER_APPENDED, () => {
              if (destroyed) return;
              attemptAutoplayWhenReady();
            });

            hls.loadSource(effectiveStreamUrl);
            hls.attachMedia(video);
            hlsRef.current = hls;
          } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
            video.src = effectiveStreamUrl;
            nativeHLSMetadataHandler = () => {
              video.currentTime = effectiveInitialPosition;
              attemptAutoplayWhenReady();
            };
            video.addEventListener("loadedmetadata", nativeHLSMetadataHandler, { once: true });
          } else {
            if (
              !reportCurrentPlanFailure({
                classification: "unsupported_transport",
                message: "The browser rejected the planned HLS transport.",
              })
            ) {
              setError("HLS playback is not supported in this browser.");
            }
          }
        } catch (error) {
          if (
            !destroyed &&
            !reportCurrentPlanFailure({
              classification: "player_initialization_error",
              message:
                error instanceof Error ? error.message : "Failed to initialize HLS playback.",
            })
          ) {
            setError("Failed to load video player.");
          }
        }
      } else {
        // Direct play — set video src directly.
        video.src = effectiveStreamUrl;
        video.currentTime = effectiveInitialPosition;
        if (shouldAutoPlay) video.play().catch(() => setPlaying(false));
      }
    }

    init();

    return () => {
      destroyed = true;
      cleanupStartupListeners();
      if (hls) {
        hls.destroy();
        hlsRef.current = null;
      }
      // Flush the video element's internal buffers so pre-downloaded
      // segments from a previous quality level don't play through
      // before the new quality takes effect.
      if (video) {
        video.removeAttribute("src");
        video.load();
      }
    };
    // `planRevision` is the single signal that the transport changed: two plans
    // can share a stream URL and still differ in protocol, timeline, or recipe.
  }, [
    effectiveStreamUrl,
    effectiveInitialPosition,
    isHlsStream,
    isPlayerReady,
    planRevision,
    plannedBitrateKbps,
    reportCurrentPlanFailure,
    shouldAutoPlay,
  ]);

  // -- Video event listeners --
  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;

    const onPlay = () => setPlaying(true);
    const onPause = () => setPlaying(false);
    const clearBuffering = () => {
      if (bufferingTimerRef.current) {
        clearTimeout(bufferingTimerRef.current);
        bufferingTimerRef.current = null;
      }
      setBuffering(false);
    };
    const markPlaybackStarted = () => {
      hlsStartupGuardRef.current?.markPlaybackStarted();
      setAwaitingFirstFrame(false);
    };
    const onTimeUpdate = () => {
      const nextTime = toMediaTime(video.currentTime, timelineOffsetRef.current);
      const resolved = resolvePendingSeekTime(nextTime, pendingSeekTime);
      setCurrentTime(resolved.currentTime);
      if (resolved.pendingSeekTime !== pendingSeekTime) {
        setPendingSeekTime(resolved.pendingSeekTime);
      }
      // timeupdate is the most reliable signal that frames are rendering.
      // Also clears any stale buffering state from HLS segment transitions
      // where `waiting` fired but `canplay`/`playing` never followed.
      markPlaybackStarted();
      clearBuffering();
    };
    const onSeeked = () => {
      setPendingSeekTime(null);
      setCurrentTime(toMediaTime(video.currentTime, timelineOffsetRef.current));
      markPlaybackStarted();
      clearBuffering();
      if (roomSyncWaiting && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportReady();
      }
    };
    const onDurationChange = () => {
      if (video.duration && isFinite(video.duration)) {
        // For HLS EVENT playlists still being written, the element reports the
        // length produced so far. The plan's source duration is the media's
        // full runtime, so it wins whenever the engine reports something short.
        if (backendDurationRef.current && video.duration < backendDurationRef.current) return;
        setDuration(video.duration);
      }
    };
    const onProgress = () => setBuffered(video.buffered);
    const onVolumeChange = () => {
      setVolume(video.volume);
      setMuted(video.muted);
      persistVolume(video.volume, video.muted);
    };
    const onWaiting = () => {
      // Delay showing the spinner so brief buffering between segments
      // or during initial HLS startup doesn't flash a spinner.
      if (!bufferingTimerRef.current) {
        bufferingTimerRef.current = setTimeout(() => {
          setBuffering(true);
          bufferingTimerRef.current = null;
        }, 500);
      }
      if (watchTogetherRoomActive && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportBuffering();
      }
    };
    const onCanPlay = () => {
      clearBuffering();
      if (roomSyncWaiting && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportReady();
      }
    };
    const onPlaying = () => {
      clearBuffering();
      markPlaybackStarted();
    };
    const onStalled = () => {
      if (watchTogetherRoomActive && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportBuffering();
      }
    };
    const onError = () => {
      if (video.error) {
        const message = video.error.message || "Unknown media element error";
        if (!reportCurrentPlanFailure({ classification: "decoder_error", message })) {
          setError(`Playback error: ${message}`);
        }
      }
    };
    const onVideoEnded = () => {
      if (endedFiredRef.current) return;
      endedFiredRef.current = true;
      setHasEnded(true);
      // Cancel any active credits countdown to prevent it racing with post-roll.
      cancelNextEpisodeAutoPlayRef.current();
      // Flush progress so the server records the final position.
      flushWatchProgressRef.current().catch(() => {});
      // Use ref to get the latest callback, avoiding stale closure issues
      // since this effect's dependency array is intentionally minimal.
      onEndedRef.current?.();
    };

    video.addEventListener("play", onPlay);
    video.addEventListener("pause", onPause);
    video.addEventListener("timeupdate", onTimeUpdate);
    video.addEventListener("seeked", onSeeked);
    video.addEventListener("durationchange", onDurationChange);
    video.addEventListener("progress", onProgress);
    video.addEventListener("volumechange", onVolumeChange);
    video.addEventListener("waiting", onWaiting);
    video.addEventListener("stalled", onStalled);
    video.addEventListener("canplay", onCanPlay);
    video.addEventListener("playing", onPlaying);
    video.addEventListener("error", onError);
    video.addEventListener("ended", onVideoEnded);

    return () => {
      video.removeEventListener("play", onPlay);
      video.removeEventListener("pause", onPause);
      video.removeEventListener("timeupdate", onTimeUpdate);
      video.removeEventListener("seeked", onSeeked);
      video.removeEventListener("durationchange", onDurationChange);
      video.removeEventListener("progress", onProgress);
      video.removeEventListener("volumechange", onVolumeChange);
      video.removeEventListener("waiting", onWaiting);
      video.removeEventListener("stalled", onStalled);
      video.removeEventListener("canplay", onCanPlay);
      video.removeEventListener("playing", onPlaying);
      video.removeEventListener("error", onError);
      video.removeEventListener("ended", onVideoEnded);
    };
    // Listener behavior depends on pending seek reconciliation. Watch-together
    // deps are intentionally narrowed to the primitives the handlers read so
    // room snapshot churn doesn't re-subscribe every listener.
  }, [
    pendingSeekTime,
    reportCurrentPlanFailure,
    roomSyncWaiting,
    sessionId,
    watchTogetherRoomActive,
    watchTogetherSync,
  ]);

  // Apply persisted volume on mount (separate from listener effect).
  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    const saved = getPersistedVolume();
    video.volume = saved.volume;
    video.muted = saved.muted;
  }, []);

  // -- Control visibility (hover anywhere to show) --
  const [controlsVisible, setControlsVisible] = useState(true);
  const hideTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clearControlsTimer = useCallback(() => {
    if (hideTimerRef.current) {
      clearTimeout(hideTimerRef.current);
      hideTimerRef.current = null;
    }
  }, []);

  const resetControlsTimer = useCallback(() => {
    setControlsVisible(true);
    clearControlsTimer();
    hideTimerRef.current = setTimeout(() => {
      if (videoRef.current && !videoRef.current.paused) {
        setControlsVisible(false);
      }
      hideTimerRef.current = null;
    }, 3000);
  }, [clearControlsTimer]);

  const hideControlsOnMouseLeave = useCallback(() => {
    clearControlsTimer();
    setControlsVisible(false);
  }, [clearControlsTimer]);

  // Show controls when paused, start hide timer when playing.
  useEffect(() => {
    if (!playing) {
      setControlsVisible(true);
      clearControlsTimer();
    } else {
      resetControlsTimer();
    }
    return clearControlsTimer;
  }, [clearControlsTimer, playing, resetControlsTimer]);

  // -- Marker editing --
  const currentMarkers = useMemo<MarkerDraft>(
    () => ({ intro, recap, credits, preview }),
    [intro, recap, credits, preview],
  );
  const markerEditor = useMarkerEditor({
    fileId: activeFileId,
    duration,
    canEdit: canEditMarkers,
    markers: currentMarkers,
    onSaved: (saved) => {
      if (activeFileId != null) onMarkersEdited?.(activeFileId, saved);
    },
  });
  // While editing, the seek bar reflects the live draft; otherwise the saved
  // props. All four kinds are shown so recap/preview are visible too.
  const markerRegions = useMemo<MarkerRegionView[]>(() => {
    const source = markerEditor.editing ? markerEditor.draft : currentMarkers;
    const out: MarkerRegionView[] = [];
    for (const kind of MARKER_KINDS) {
      const range = source[kind];
      if (range && range.end > range.start) {
        out.push({ kind, start: range.start, end: range.end });
      }
    }
    return out;
  }, [markerEditor.editing, markerEditor.draft, currentMarkers]);

  // -- Playback info overlay --
  const [showPlaybackInfo, setShowPlaybackInfo] = useState(false);

  // -- Fullscreen tracking --
  useEffect(() => {
    const onChange = () => setIsFullscreen(!!document.fullscreenElement);
    document.addEventListener("fullscreenchange", onChange);
    return () => document.removeEventListener("fullscreenchange", onChange);
  }, []);

  // -- Subtitle appearance --
  const { settings: subtitleSettings, containerStyle, cueStyle } = useSubtitleAppearance();
  const { positionStyle: subtitlePositionStyle, fontScale: subtitleFontScale } = useSubtitleLayout(
    containerRef,
    videoRef,
    subtitleSettings.position,
  );
  // Scale cue text with the rendered video so subtitles stay proportionally
  // the same size as the window grows or shrinks.
  const scaledCueStyle = useMemo(
    () => ({
      ...cueStyle,
      fontSize: computeSubtitleFontSize(subtitleSettings.fontSize, subtitleFontScale),
    }),
    [cueStyle, subtitleSettings.fontSize, subtitleFontScale],
  );

  // Measure the bottom control bar so bottom-anchored subtitles can lift just
  // above it while it's visible (YouTube-style) instead of hiding behind it.
  // The base offset scales with the player, but the bar is a roughly fixed
  // pixel height, so measure rather than hardcode. The .player-hud element is
  // laid out even while the controls are faded out, so its height is readable
  // regardless of visibility.
  const [controlBarHeight, setControlBarHeight] = useState(0);
  useEffect(() => {
    const container = containerRef.current;
    if (!container || isDetached) return;
    const hud = container.querySelector<HTMLElement>(".player-hud");
    if (!hud) return;
    const update = () => setControlBarHeight(hud.offsetHeight);
    update();
    const ro = new ResizeObserver(update);
    ro.observe(hud);
    return () => ro.disconnect();
  }, [isDetached, isPlayerReady, displayMode]);

  // Bottom-anchored cues rise to clear the control bar (plus a small gap) only
  // while the bar is up and only in the main foreground player; top-anchored
  // cues never collide with the bottom HUD, so they don't move.
  const SUBTITLE_HUD_GAP = 12;
  const baseSubtitleBottomPx = (() => {
    const raw = subtitlePositionStyle.bottom;
    if (typeof raw === "number") return Number.isFinite(raw) ? raw : 0;
    if (typeof raw === "string" && raw.endsWith("px")) {
      const parsed = parseFloat(raw);
      return Number.isFinite(parsed) ? parsed : 0;
    }
    return 0;
  })();
  const subtitlesLifted =
    displayMode === "foreground" &&
    controlsVisible &&
    subtitleSettings.position !== "top" &&
    controlBarHeight > 0;
  const subtitleLiftPx = subtitlesLifted
    ? Math.max(0, controlBarHeight + SUBTITLE_HUD_GAP - baseSubtitleBottomPx)
    : 0;

  // -- Subtitle cue matching --
  // Returns active cue texts for custom rendering instead of native TextTrack
  // (which has browser bugs with stale cues persisting after seek).
  const activeCueTexts = useSubtitleTracks(
    videoRef,
    effectiveSubtitleTracks,
    activeSubtitleIndex,
    timelineOffsetSeconds,
    subtitleDelayMs,
    durationRef,
    subtitleFetchAnchorRef,
    liveCues,
    liveTranslation?.trackKey ?? null,
    subtitleStreamGeneration,
  );

  // -- ASS/SSA subtitle rendering via JASSUB (client-side libass) --
  const { isActive: isASSActive } = useASSSubtitles(
    videoRef,
    subtitleUrls,
    activeSubtitleIndex,
    isDetached,
    timelineOffsetSeconds,
    subtitleDelayMs,
  );

  // -- Authoritative subtitle track selection --
  // Some tracks (bitmap PGS/DVD/DVB) cannot be delivered as a sidecar and are
  // published `burn_in_only`: the server composites them into the video. The
  // client does not decide that and does not translate ordinals for it — it
  // asks for the track by the server's own combined ordinal and the plan comes
  // back with `subtitle.mode === "burn_in"`.
  const activeSubtitleTrack =
    activeSubtitleIndex !== null
      ? (effectiveSubtitleTracks.find((track) => track.index === activeSubtitleIndex) ?? null)
      : null;
  const requestedSubtitleTrackChangeRef = useRef<string | null>(null);
  useEffect(() => {
    const desiredServerIndex = pendingServerSubtitleSelection(
      plan.subtitle.mode,
      plan.selected_tracks.subtitle?.index ?? null,
      activeSubtitleIndex,
      activeSubtitleTrack?.burn_in_only === true,
    );
    if (desiredServerIndex === undefined) {
      requestedSubtitleTrackChangeRef.current = null;
      return;
    }

    const requestKey = `${plan.plan_id}:${desiredServerIndex ?? "none"}`;
    if (requestedSubtitleTrackChangeRef.current === requestKey) return;
    requestedSubtitleTrackChangeRef.current = requestKey;

    // Until the element has media loaded (an auto-selected bitmap preference at
    // session start, or a stream reload), currentTime still reads 0 rather than
    // the resume/seek target — use the intended position, as the subtitle
    // window fetcher does.
    const position = subtitleStartPositionSeconds(
      videoRef.current?.readyState ?? 0,
      currentTimeRef.current,
      subtitleFetchAnchorRef.current,
    );
    onSubtitleTrackChange?.(desiredServerIndex, position);
  }, [
    activeSubtitleIndex,
    activeSubtitleTrack?.burn_in_only,
    onSubtitleTrackChange,
    plan.plan_id,
    plan.selected_tracks.subtitle?.index,
    plan.subtitle.mode,
  ]);

  // A refused replan leaves the previous stream playing, so the selection has
  // to roll back too: without this the menu would keep claiming a track is on
  // that the server never rendered, and re-picking it would be suppressed as an
  // unchanged selection instead of retrying. Pin the accepted selection just
  // like a manual choice so auto-selection does not immediately request the
  // rejected track again; a later user choice can still retry it explicitly.
  // The rollback is silent otherwise: the refusal is only rendered inside the
  // quality menu, which the user has no reason to open after picking a
  // subtitle. Clearing the request ref keeps this to one toast per refusal.
  useEffect(() => {
    if (requestedSubtitleTrackChangeRef.current && replanError && !replanning) {
      subtitleSelectionWasManualRef.current = true;
      setActiveSubtitleIndex(plan.selected_tracks.subtitle?.index ?? null);
      requestedSubtitleTrackChangeRef.current = null;
      toast.error(replanErrorTitle ?? "That subtitle track can't be used", {
        description: replanError,
      });
    }
  }, [plan.selected_tracks.subtitle?.index, replanError, replanErrorTitle, replanning]);

  // A refusal pin belongs only to the session that rejected the automatic
  // selection. Clear it before the auto-selection effect evaluates a new
  // session so the viewer's persisted subtitle mode applies to the next title.
  useEffect(() => {
    subtitleSelectionWasManualRef.current = false;
  }, [sessionId]);

  // -- Auto-select subtitle track based on mode --
  useEffect(() => {
    if (subtitleSelectionWasManualRef.current) {
      const selectionStillExists =
        activeSubtitleIndex === null ||
        effectiveSubtitleTracks.some((track) => track.index === activeSubtitleIndex);
      if (selectionStillExists) {
        return;
      }
      subtitleSelectionWasManualRef.current = false;
    }

    if (effectiveSubtitleTracks.length === 0) {
      setActiveSubtitleIndex(null);
      lastSubtitleIndexRef.current = null;
      return;
    }

    const effectiveMode = normalizeSubtitleMode(subtitleMode);
    const audioLang =
      audioTracks[activeAudioIndex]?.language?.trim() ||
      resolveVersionAudioLanguage(selectedVersion, activeAudioIndex);

    const match = resolveSubtitleAutoSelect({
      mode: effectiveMode,
      tracks: effectiveSubtitleTracks,
      preferredLanguage: preferredSubtitleLanguage ?? null,
      preferredTrackSignature: preferredSubtitleTrackSignature ?? null,
      audioLanguage: audioLang,
      profileLanguage: profileLanguage ?? null,
      showForcedSubtitles: showForcedSubtitles ?? true,
    });

    if (match !== null) {
      setActiveSubtitleIndex(match);
      lastSubtitleIndexRef.current = match;
      return;
    }
    setActiveSubtitleIndex(null);
    lastSubtitleIndexRef.current = null;
  }, [
    activeSubtitleIndex,
    preferredSubtitleLanguage,
    preferredSubtitleTrackSignature,
    effectiveSubtitleTracks,
    subtitleMode,
    showForcedSubtitles,
    profileLanguage,
    audioTracks,
    activeAudioIndex,
    selectedVersion,
    sessionId,
  ]);

  // -- Control callbacks --
  const handlePlayPause = useCallback(() => {
    const video = videoRef.current;
    if (!video) return;
    if (
      watchTogetherRoomId &&
      !watchTogether.closedReason &&
      (watchTogether.connectionState !== "connected" || !watchTogether.room)
    ) {
      showWatchTogetherNotice(
        "Reconnecting to room. Controls are temporarily unavailable.",
        "warning",
      );
      return;
    }
    if (watchTogether.room && !watchTogether.room.self_can_control_transport) {
      showWatchTogetherNotice("Only the host can control playback.", "warning");
      return;
    }
    if (watchTogether.room && watchTogetherSync.attachedSessionId !== sessionId) {
      showWatchTogetherNotice("Joining room playback. Try again in a moment.", "info");
      return;
    }

    if (watchTogether.room) {
      watchTogetherSync.requestTransport(
        video.paused ? "play" : "pause",
        currentTimeRef.current,
        !video.paused,
      );
      return;
    }

    if (video.paused) {
      video.play().catch(() => {});
      return;
    }

    video.pause();
  }, [sessionId, showWatchTogetherNotice, watchTogether, watchTogetherRoomId, watchTogetherSync]);

  useEffect(() => {
    reportRoomReadyRef.current = watchTogetherSync.reportReady;
  }, [watchTogetherSync.reportReady]);

  useEffect(() => {
    const command = watchTogether.transportCommand;
    const roomSelectionRevision = watchTogether.room?.selection_revision;
    if (
      !command ||
      roomSelectionRevision === undefined ||
      roomSelectionRevision === null ||
      !sessionId
    ) {
      return;
    }
    if (command.command_id === lastRoomCommandIdRef.current) {
      return;
    }
    if (command.session_id && command.session_id !== sessionId) {
      return;
    }
    if (command.selection_revision !== roomSelectionRevision) {
      return;
    }

    lastRoomCommandIdRef.current = command.command_id;

    if (roomCommandTimerRef.current !== null) {
      window.clearTimeout(roomCommandTimerRef.current);
      roomCommandTimerRef.current = null;
    }

    const serverExecuteAt = Date.parse(command.execute_at);
    const localExecuteAt = Number.isFinite(serverExecuteAt)
      ? serverExecuteAt - watchTogether.serverTimeOffsetMs
      : Date.now();
    const delay = Math.max(0, localExecuteAt - Date.now());

    roomCommandTimerRef.current = window.setTimeout(() => {
      roomCommandTimerRef.current = null;
      void (async () => {
        const video = videoRef.current;
        if (!video) {
          return;
        }

        const delta = Math.abs(currentTimeRef.current - command.position_seconds);
        if (command.action === "seek" || delta > 0.35) {
          performPlayerSeekRef.current(command.position_seconds);
        }

        if (command.action === "pause" || command.action === "seek") {
          video.pause();
        }

        if (command.action === "play") {
          await video.play();
        }

        if (
          command.playback_state === "waiting" &&
          command.action === "pause" &&
          video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA
        ) {
          reportRoomReadyRef.current(command.position_seconds, true);
        }
      })().catch(() => {});
    }, delay);

    return () => {
      if (roomCommandTimerRef.current !== null) {
        window.clearTimeout(roomCommandTimerRef.current);
        roomCommandTimerRef.current = null;
      }
    };
  }, [
    sessionId,
    watchTogether.room?.selection_revision,
    watchTogether.serverTimeOffsetMs,
    watchTogether.transportCommand,
  ]);

  const handleVolumeChange = useCallback((v: number) => {
    const video = videoRef.current;
    if (!video) return;
    video.volume = v;
    if (v > 0 && video.muted) video.muted = false;
  }, []);

  const handleMutedChange = useCallback((m: boolean) => {
    const video = videoRef.current;
    if (!video) return;
    video.muted = m;
  }, []);

  const handleFullscreenToggle = useCallback(() => {
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
    } else {
      containerRef.current?.requestFullscreen().catch(() => {});
    }
  }, []);

  // -- Keyboard shortcuts --
  useKeyboardShortcuts(
    videoRef,
    containerRef,
    handlePlayPause,
    handleKeyboardSeek,
    toggleCaptions,
    handleTogglePiP,
    displayMode === "foreground",
  );

  // The id is the plan's own quality label, handed back to the server verbatim.
  const handleQualitySelect = useCallback(
    (id: string) => {
      onQualitySelect?.(id, currentTime);
    },
    [currentTime, onQualitySelect],
  );

  const handlePlayPauseRef = useRef(handlePlayPause);
  const handlePlayerSeekRef = useRef(handlePlayerSeek);
  const handleTogglePiPRef = useRef(handleTogglePiP);

  useEffect(() => {
    handlePlayPauseRef.current = handlePlayPause;
  }, [handlePlayPause]);

  useEffect(() => {
    handlePlayerSeekRef.current = handlePlayerSeek;
  }, [handlePlayerSeek]);

  useEffect(() => {
    handleTogglePiPRef.current = handleTogglePiP;
  }, [handleTogglePiP]);

  useEffect(() => {
    if (!onPlaybackStateChange) {
      return;
    }

    onPlaybackStateChange({
      currentTime,
      duration,
      playing,
    });
  }, [currentTime, duration, onPlaybackStateChange, playing]);

  useEffect(() => {
    if (!onPlaybackTransportReady) {
      return;
    }

    const transport: PlayerPlaybackTransport = {
      playPause: () => {
        handlePlayPauseRef.current();
      },
      seekBy: (secondsDelta: number) => {
        const nextCurrentTime = currentTimeRef.current;
        const nextDuration = durationRef.current;
        const maxTime = nextDuration > 0 ? nextDuration : nextCurrentTime + Math.abs(secondsDelta);
        handlePlayerSeekRef.current(Math.max(0, Math.min(maxTime, nextCurrentTime + secondsDelta)));
      },
      seekTo: (seconds: number) => {
        handlePlayerSeekRef.current(seconds);
      },
      togglePictureInPicture: () => handleTogglePiPRef.current(),
    };

    onPlaybackTransportReady(transport);
    return () => onPlaybackTransportReady(null);
  }, [onPlaybackTransportReady]);

  const executeRealtimeCommand = useCallback(
    async (command: PlaybackRealtimeCommandEnvelope) => {
      const video = videoRef.current;

      switch (command.name) {
        case "pause":
          video?.pause();
          return;
        case "unpause":
          if (!video) return;
          await video.play();
          return;
        case "play_pause":
          if (!video) return;
          if (video.paused) {
            await video.play();
          } else {
            video.pause();
          }
          return;
        case "seek": {
          const position = readNumericPayload(
            command.payload,
            "position",
            "position_seconds",
            "seconds",
          );
          if (position === null) {
            throw new Error("missing_seek_position");
          }
          performPlayerSeek(position);
          return;
        }
        case "set_volume": {
          const nextVolume = readNumericPayload(command.payload, "volume", "level");
          if (nextVolume === null || !video) {
            throw new Error("missing_volume");
          }
          video.volume = Math.min(1, Math.max(0, nextVolume));
          if (video.volume > 0 && video.muted) {
            video.muted = false;
          }
          return;
        }
        case "display_message":
          setNotice({
            title: readStringPayload(command.payload, "title") ?? "Playback notice",
            message:
              readStringPayload(command.payload, "message") ?? "A server message was received.",
            tone: "info",
          });
          return;
        case "server_restarting":
          setNotice({
            title: readStringPayload(command.payload, "title") ?? "Server restarting",
            message:
              readStringPayload(command.payload, "message") ??
              "Playback may end shortly while the server restarts.",
            tone: "warning",
          });
          return;
        case "server_shutting_down":
          setNotice({
            title: readStringPayload(command.payload, "title") ?? "Server shutting down",
            message:
              readStringPayload(command.payload, "message") ??
              "Playback may end shortly while the server shuts down.",
            tone: "warning",
          });
          return;
        case "stop":
        case "terminate":
          if (command.payload) {
            const message = readStringPayload(command.payload, "message");
            if (message) {
              setNotice({
                title:
                  readStringPayload(command.payload, "title") ??
                  (command.name === "terminate" ? "Playback ended" : "Playback stopping"),
                message,
                tone: "warning",
              });
            }
          }
          await handleExit();
          return;
        default:
          throw new Error("unsupported");
      }
    },
    [handleExit, performPlayerSeek],
  );

  const realtime = usePlaybackRealtime({
    sessionId,
    onCommand: executeRealtimeCommand,
    onEvent: handleRealtimeEvent,
  });

  useEffect(() => {
    onRealtimeConnectionStateChange?.(realtime.connectionState);
  }, [onRealtimeConnectionStateChange, realtime.connectionState]);

  // -- Postroll mini-player resize --
  const [miniPlayerWidth, setMiniPlayerWidth] = useState(320);
  const isDraggingRef = useRef(false);

  const handleResizePointerDown = useCallback(
    (e: React.PointerEvent) => {
      e.preventDefault();
      e.stopPropagation();
      isDraggingRef.current = true;
      const startX = e.clientX;
      const startWidth = miniPlayerWidth;
      const target = e.currentTarget as HTMLElement;
      target.setPointerCapture(e.pointerId);

      const onMove = (ev: PointerEvent) => {
        // Handle is at bottom-right; dragging right grows the player.
        const delta = ev.clientX - startX;
        setMiniPlayerWidth(Math.max(200, Math.min(640, startWidth + delta)));
      };
      const onUp = () => {
        isDraggingRef.current = false;
        target.removeEventListener("pointermove", onMove);
        target.removeEventListener("pointerup", onUp);
      };
      target.addEventListener("pointermove", onMove);
      target.addEventListener("pointerup", onUp);
    },
    [miniPlayerWidth],
  );

  const handleMiniPlayerClick = useCallback(() => {
    if (isDraggingRef.current) return;
    onReturnFromPostRoll?.();
  }, [onReturnFromPostRoll]);

  const handleCopyWatchTogetherInvite = useCallback(async () => {
    const copied = await copyWatchTogetherInvite(
      watchTogether.room?.invite_path,
      watchTogether.room?.code,
    );
    if (!copied) {
      showWatchTogetherNotice("Invite link is not ready yet.", "info");
    }
  }, [showWatchTogetherNotice, watchTogether.room]);

  const handleToggleGuestControl = useCallback(
    async (policy: "host_only" | "guest_play_pause") => {
      await setWatchTogetherGuestControl(watchTogether.updatePolicy, policy);
    },
    [watchTogether],
  );

  const handleEndRoom = useCallback(async () => {
    await endWatchTogetherRoom(watchTogether.closeRoom);
  }, [watchTogether]);

  // -- Render --

  const isPostrollVisible = displayMode === "postroll" && !hasEnded;

  return (
    <div
      ref={containerRef}
      className={
        displayMode === "postroll"
          ? `player-container fixed top-6 left-6 z-[60] aspect-video overflow-hidden rounded-2xl bg-black shadow-2xl ring-1 ring-white/10 transition-opacity duration-700 ${hasEnded ? "pointer-events-none opacity-0" : "cursor-pointer"}`
          : isDetached
            ? "pointer-events-none fixed top-0 left-0 z-[-1] h-px w-px overflow-hidden opacity-0"
            : controlsVisible
              ? "player-container fixed inset-0 z-50 bg-black"
              : "player-container fixed inset-0 z-50 cursor-none bg-black"
      }
      style={displayMode === "postroll" ? { width: miniPlayerWidth } : undefined}
      onClick={isPostrollVisible ? handleMiniPlayerClick : undefined}
      onMouseEnter={isDetached ? undefined : resetControlsTimer}
      onMouseLeave={isDetached ? undefined : hideControlsOnMouseLeave}
      onMouseMove={isDetached ? undefined : resetControlsTimer}
    >
      {/* Postroll resize handle (bottom-left corner) */}
      {isPostrollVisible && (
        <div
          onPointerDown={handleResizePointerDown}
          className="absolute right-0 bottom-0 z-10 flex h-6 w-6 cursor-nwse-resize items-end justify-end p-1 opacity-0 transition-opacity hover:opacity-100"
          onClick={(e) => e.stopPropagation()}
        >
          <svg width="10" height="10" viewBox="0 0 10 10" className="text-white/50">
            <path
              d="M10 10L0 0M10 10L4 10M10 10L10 4"
              stroke="currentColor"
              strokeWidth="1.5"
              fill="none"
            />
          </svg>
        </div>
      )}
      {/* Back button + media info */}
      {!isDetached && (
        <div
          className={`absolute top-4 left-4 z-50 flex items-center gap-3 transition-opacity duration-300 ${
            controlsVisible ? "opacity-100" : "pointer-events-none opacity-0"
          }`}
        >
          <button
            onClick={() => {
              void handleMinimize();
            }}
            disabled={isLeaving}
            aria-label="Minimize player"
            title="Minimize player"
            className="flex h-11 w-11 items-center justify-center rounded-full bg-black/60 text-white hover:bg-black/80"
            type="button"
          >
            <svg
              aria-hidden="true"
              xmlns="http://www.w3.org/2000/svg"
              width="20"
              height="20"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="m6 9 6 6 6-6" />
            </svg>
          </button>
          <button
            onClick={() => {
              void handleExit();
            }}
            disabled={isLeaving}
            className="flex items-center gap-2 rounded-full bg-black/60 px-4 py-2 text-sm text-white hover:bg-black/80"
            type="button"
          >
            <svg
              aria-hidden="true"
              xmlns="http://www.w3.org/2000/svg"
              width="20"
              height="20"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M18 6 6 18" />
              <path d="m6 6 12 12" />
            </svg>
            Exit
          </button>
          {/* Title + episode info have moved to the bottom HUD in the
              redesigned player — the top-left chrome now carries only the
              minimize and exit affordances. Keep a screen-reader-only label
              so the exit button still communicates what playback context
              it leaves from. */}
          <div className="sr-only">
            {seriesContext ? (
              <>
                <span>
                  {seriesContext.seriesTitle ?? title}
                  {year ? ` (${year})` : ""}
                </span>
                <span>
                  S{seriesContext.currentSeason}:E{seriesContext.currentEpisode}
                  {title ? ` · ${title}` : ""}
                </span>
              </>
            ) : (
              <span>
                {title}
                {year ? ` (${year})` : ""}
              </span>
            )}
          </div>
        </div>
      )}

      {!isDetached && watchTogetherRoomId && !watchTogether.closedReason ? (
        <WatchTogetherPanel
          room={watchTogether.room}
          connectionState={watchTogether.connectionState}
          visible={controlsVisible}
          onCopyInvite={() => void handleCopyWatchTogetherInvite()}
          onToggleGuestControl={(policy) => void handleToggleGuestControl(policy)}
          onEndRoom={() => void handleEndRoom()}
        />
      ) : null}

      {/* Loading overlay — stays up until the first frame renders */}
      {!isDetached && (awaitingFirstFrame || !isPlayerReady) && !error && (
        <div
          role="status"
          aria-label="Loading video"
          className="absolute inset-0 z-40 flex items-center justify-center bg-black"
        >
          <div className="h-8 w-8 animate-spin rounded-full border-2 border-white/20 border-t-white" />
          <span className="sr-only">Loading video</span>
        </div>
      )}

      {/* Room sync overlay */}
      {!isDetached && roomSyncWaiting && !awaitingFirstFrame && isPlayerReady && (
        <div
          role="status"
          aria-label="Syncing playback"
          className="pointer-events-none absolute inset-0 z-30 flex items-center justify-center px-6"
        >
          <div className="rounded-[8px] border border-white/15 bg-black/70 px-5 py-4 text-center text-white shadow-2xl backdrop-blur">
            <div className="mx-auto h-10 w-10 animate-spin rounded-full border-2 border-white/20 border-t-white" />
            <div className="mt-3 text-sm font-medium">Syncing playback</div>
            <div className="mt-1 text-xs text-white/70">
              Buffering and syncing all users before resuming.
            </div>
          </div>
        </div>
      )}

      {/* Buffering spinner (mid-playback stalls only) */}
      {!isDetached && buffering && !roomSyncWaiting && !awaitingFirstFrame && isPlayerReady && (
        <div
          role="status"
          aria-label="Buffering"
          className="pointer-events-none absolute inset-0 z-30 flex items-center justify-center"
        >
          <div className="h-10 w-10 animate-spin rounded-full border-2 border-white/20 border-t-white" />
          <span className="sr-only">Buffering</span>
        </div>
      )}

      {!isDetached && notice ? (
        <PlaybackNoticeOverlay title={notice.title} message={notice.message} tone={notice.tone} />
      ) : null}

      {/* Error state */}
      {!isDetached && error && (
        <div className="absolute inset-0 z-40 flex items-center justify-center bg-black/80">
          <div className="text-center">
            <div className="mb-4 text-sm text-white/60">{error}</div>
            <button
              onClick={() => {
                void handleExit();
              }}
              disabled={isLeaving}
              type="button"
              className="rounded bg-white/10 px-4 py-2 text-sm text-white hover:bg-white/20"
            >
              Go Back
            </button>
          </div>
        </div>
      )}

      {/* Video element — always rendered so the ref stays stable for
          event listeners and hls.js across quality switches. */}
      {/* Subtitle tracks are managed programmatically by useSubtitleTracks
          instead of <track> elements, so subtitle rendering stays on the same
          media timeline as restarted HLS playback. */}
      <video
        ref={videoRef}
        className={isDetached ? "h-full w-full" : "absolute inset-0 h-full w-full"}
        onClick={displayMode === "postroll" ? undefined : handlePlayPause}
        playsInline
        style={!isPlayerReady ? { visibility: "hidden" } : undefined}
      />

      {/* Subtitle overlay — suppressed when JASSUB (ASS) is rendering; bitmap
          tracks are burned into the video server-side and never reach here.
          While the control bar is up, bottom-anchored cues rise just above it
          (subtitleLiftPx) so they never overlap the HUD; they settle back when
          it hides. z-[5] keeps cues below the controls layer (z-10) as a
          safety, so any residual overlap tucks behind the bar rather than
          painting on top of it. */}
      {!isDetached && !isASSActive && activeCueTexts.length > 0 && (
        <div
          className="pointer-events-none absolute inset-x-0 z-[5] flex flex-col items-center gap-1"
          style={{
            ...containerStyle,
            ...subtitlePositionStyle,
            transform: `translateY(-${subtitleLiftPx}px)`,
            transition: "transform 200ms ease",
          }}
        >
          {activeCueTexts.map((text, i) => (
            <span
              key={i}
              className="inline-block rounded px-3 py-1 text-center leading-snug"
              style={{ ...scaledCueStyle, whiteSpace: "pre-line" }}
            >
              {text}
            </span>
          ))}
        </div>
      )}

      {/* Intro skip button */}
      {!isDetached && showIntroSkip && <IntroSkipButton onSkip={skipIntro} />}
      {!isDetached && showRecapSkip && <IntroSkipButton onSkip={skipRecap} label="Skip Recap" />}

      {/* Marker editor */}
      {!isDetached && markerEditor.editing && (
        <MarkerEditPanel editor={markerEditor} currentTime={currentTime} />
      )}

      {/* Next episode overlay */}
      {!isDetached && nextEpisode.showCountdown && nextEpisode.nextEpisode && (
        <NextEpisodeOverlay
          episode={nextEpisode.nextEpisode}
          secondsRemaining={nextEpisode.secondsRemaining}
          onSkip={nextEpisode.skipToNext}
          onCancel={nextEpisode.cancelAutoPlay}
        />
      )}

      {/* Live translation buffering indicator */}
      {translationBuffering && (
        <div className="pointer-events-none absolute inset-0 z-30 flex items-center justify-center">
          <div className="flex items-center gap-3 rounded-lg bg-black/80 px-4 py-3 text-sm text-white shadow-lg">
            <span className="h-4 w-4 animate-spin rounded-full border-2 border-white/30 border-t-white" />
            Preparing {liveTranslation?.label || "translated"} subtitles…
          </div>
        </div>
      )}

      {/* Controls */}
      {!isDetached && isPlayerReady && (
        <PlayerControls
          visible={controlsVisible || markerEditor.editing}
          playing={playing}
          currentTime={currentTime}
          duration={duration}
          buffered={buffered}
          chapters={chapters}
          regions={markerRegions}
          editing={markerEditor.editing}
          activeEditKind={markerEditor.activeKind}
          onRegionEdgeChange={markerEditor.setEdge}
          markerEditAvailable={markerEditor.canEdit}
          markerEditActive={markerEditor.editing}
          onToggleMarkerEdit={markerEditor.editing ? markerEditor.cancel : markerEditor.begin}
          volume={volume}
          muted={muted}
          isFullscreen={isFullscreen}
          subtitleTracks={effectiveSubtitleTracks}
          activeSubtitleIndex={activeSubtitleIndex}
          onSubtitleSelect={handleSubtitleSelect}
          subtitleDelayMs={subtitleDelayMs}
          onSubtitleDelayChange={setSubtitleDelayMs}
          mediaFileId={activeFileId ?? undefined}
          playerConfig={playerConfig}
          onRefreshSubtitles={
            onRefreshSubtitles ? () => onRefreshSubtitles(getSubtitleStartPosition()) : undefined
          }
          sessionId={sessionId}
          getSubtitleStartPosition={getSubtitleStartPosition}
          audioTracks={audioTracks}
          activeAudioIndex={activeAudioIndex}
          onAudioSelect={onAudioSelect}
          qualityOptions={qualityOptions}
          activeQualityId={activeQualityId}
          isTranscoding={replanning}
          qualityError={replanError}
          onQualitySelect={handleQualitySelect}
          versions={
            versions.length > 1
              ? versions.map((v) => ({
                  fileId: v.file_id,
                  label: `${v.resolution} ${v.codec_video.toUpperCase()}${v.hdr ? " HDR" : ""}`,
                  // The server names the file it actually planned against; a
                  // fallback to an alternate version shows up here.
                  isCurrentSource: v.file_id === plan.effective_media_file_id,
                  isRequestedSource: v.file_id === plan.requested_media_file_id,
                }))
              : undefined
          }
          onSwitchVersion={
            onSwitchVersion ? (fileId) => onSwitchVersion(fileId, currentTime) : undefined
          }
          onTogglePiP={handleTogglePiP}
          onPlayPause={handlePlayPause}
          onSeek={handlePlayerSeek}
          onVolumeChange={handleVolumeChange}
          onMutedChange={handleMutedChange}
          onFullscreenToggle={handleFullscreenToggle}
          showPlaybackInfo={showPlaybackInfo}
          onTogglePlaybackInfo={() => setShowPlaybackInfo((v) => !v)}
          hasPrevEpisode={!!prevEpisodeRef}
          hasNextEpisode={!!nextEpisode.nextEpisode}
          onPrevEpisode={goToPrevEpisode}
          onNextEpisode={nextEpisode.skipToNext}
          title={hudTitle}
          subtitleLabel={hudSubtitle}
        />
      )}

      {/* Playback info overlay */}
      {!isDetached && showPlaybackInfo && (
        <PlaybackInfoOverlay
          videoRef={videoRef}
          containerRef={containerRef}
          streamUrl={effectiveStreamUrl}
          plan={plan}
          currentSourceVersion={effectiveVersion}
          requestedVersion={selectedVersion}
          onClose={() => setShowPlaybackInfo(false)}
        />
      )}
    </div>
  );
}
