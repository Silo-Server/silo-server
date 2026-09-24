import { useMediaSkipHandlers } from "../hooks/useMediaSkipHandlers";
import { skipTarget } from "../utils/skipTarget";
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
import { useIntroSkipPrompt } from "../hooks/useIntroSkipPrompt";
import { useRemuxSeeking } from "../hooks/useRemuxSeeking";
import { useSubtitleTracks } from "../hooks/useSubtitleTracks";
import { useASSSubtitles } from "../hooks/useASSSubtitles";
import { useSubtitleAppearance } from "../hooks/useSubtitleAppearance";
import { useSubtitleLayout } from "../hooks/useSubtitleLayout";
import { useCoarsePointer } from "../hooks/useCoarsePointer";
import { computeSubtitleFontSize } from "@/lib/subtitleAppearance";
import { useNextEpisode } from "../hooks/useNextEpisode";
import { MARKER_KINDS, useMarkerEditor } from "../hooks/useMarkerEditor";
import { useWatchTogetherPlaybackSync } from "../hooks/useWatchTogetherPlaybackSync";
import type { WatchTogetherRoomConnectionResult } from "../hooks/useWatchTogetherRoomConnection";
import { getPersistedVolume, persistVolume } from "./VolumeControl";
import { playerV2 } from "../player-v2";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import { lowerQualityOption, qualityOptionsFromPlanV3 } from "../playback-info";
import { preconnectToStreamOrigin } from "../stream-url";
import { WatchTogetherPanel } from "./WatchTogetherPanel";
import { readPlanInvalidatedPayload, VIDEO_PLAYBACK_COMMANDS } from "../realtime-protocol";
import type {
  PlaybackRealtimeCommandEnvelope,
  PlaybackRealtimeEventEnvelope,
} from "../realtime-protocol";
import { resolvePendingSeekTime } from "../utils/pendingSeek";
import { resolveVersionAudioLanguage } from "../utils/effectiveAudioLanguage";
import { HlsStartupGuard } from "../utils/hlsStartupGuard";
import { isSafariBrowserV3, resolveHLSEngineV3 } from "../utils/hlsEngine";
import { isFirefoxUserAgent } from "../utils/browser";
import { normalizeSubtitleMode } from "../utils/subtitleMode";
import {
  markerOccurrenceAtTime,
  resolveAutoplayMarker,
  resolveMarkerRegions,
} from "../utils/watchPageMarkers";
import type {
  PlaybackExitState,
  IntroSkipMode,
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
  PlayerMarkerSegment,
  MarkerDraft,
  MarkerKind,
  MarkerRegionView,
  PlaybackStartTrigger,
  SeriesContext,
  SubtitleMode,
  VideoFitMode,
} from "../types";
import type { FailureV3, PlanV3, SubtitleInventoryItemV3 } from "../protocol-v3";
import {
  mediaDurationSeconds,
  subtitleStartPositionSeconds,
  toMediaTime,
  toPlayerTime,
} from "../utils/mediaTimeline";
import {
  abandonRoomReload,
  beginRoomReload,
  createRoomReloadBudget,
  decideRoomCatchup,
  isNativePositionInRanges,
  landRoomReload,
  noteRoomReloadLoading,
  roomCatchupConverged,
  roomCatchupExpectedPosition,
  roomReloadAllowed,
  roomMembersLeftBehind,
  roomReloadLanded,
  settleRoomReloads,
  type RoomCatchupTarget,
} from "../utils/roomSyncCatchup";
import { pendingServerSubtitleSelection } from "../utils/playableSubtitles";
import {
  copyWatchTogetherInvite,
  endWatchTogetherRoom,
  stopWatchTogetherPlayback,
  setWatchTogetherGuestControl,
} from "@/lib/watchTogetherActions";
import { toast } from "sonner";

let hlsJSModule: Promise<typeof HlsType> | null = null;

function loadHLSJS(): Promise<typeof HlsType> {
  hlsJSModule ??= import("hls.js").then(
    (module) => module.default,
    (error) => {
      // Drop the memoized rejection so a later playback retries the fetch.
      hlsJSModule = null;
      throw error;
    },
  );
  return hlsJSModule;
}

// Warm the hls.js chunk at module load so the first playback's time to first
// frame doesn't pay the cold dynamic-import latency on top of plan resolution.
// A failed warm-up stays quiet here; playback retries and reports it.
void loadHLSJS().catch(() => {});

// Reserved index for the in-progress live AI translation track. Sits well above
// any real subtitle index so it never collides.
const LIVE_SUBTITLE_INDEX = 1_000_000;
// Resume playback once translated cues cover at least this far ahead of the
// playhead; a hard cap also resumes so we never wait forever.
const TRANSLATION_RESUME_TIMEOUT_MS = 30_000;
const MARKER_SKIP_LABELS: Record<MarkerKind, string> = {
  intro: "Skip Intro",
  recap: "Skip Recap",
  credits: "Skip Credits",
  preview: "Skip Preview",
};

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
  /**
   * Replan for a plan the server invalidated over the realtime
   * `plan_invalidated` command. Resolving false rejects the command, which is
   * what tells the server to stop the session instead.
   */
  onPlanInvalidated?: (planId: string, reason: string, currentPosition: number) => Promise<boolean>;
  /**
   * `seek_reanchor` replan when a seek target falls outside the seekable window.
   * Reports whether the replan landed a plan at the requested position.
   */
  onReanchorSeek?: (positionSeconds: number) => boolean | Promise<boolean>;
  preferredSubtitleLanguage?: string | null;
  preferredSubtitleTrackSignature?: PlayerSubtitleTrackSignature | null;
  subtitleMode?: SubtitleMode;
  showForcedSubtitles?: boolean;
  profileLanguage?: string | null;
  intro: PlayerTimeRange | null;
  /**
   * null means the connected server's answer is not in yet, which is not the
   * same as "ask": prompting on a guess can skip an intro the viewer told the
   * server to leave alone.
   */
  introSkipMode?: IntroSkipMode | null;
  credits: PlayerTimeRange | null;
  recap?: PlayerTimeRange | null;
  autoSkipRecap?: boolean;
  preview?: PlayerTimeRange | null;
  markerSegments?: PlayerMarkerSegment[];
  autoPlayNextPreview?: boolean;
  canEditMarkers?: boolean;
  /** Notified after a successful in-player marker edit so the host can patch local state. */
  onMarkersEdited?: (fileId: number, markers: MarkerDraft) => void;
  duration?: number;
  seriesContext?: SeriesContext;
  onNavigateEpisode?: (contentId: string, trigger: PlaybackStartTrigger) => void;
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
  /** Profile rewind/fast-forward intervals; the host resolves contract defaults for old servers. */
  seekIntervals: { back: number; forward: number };
  displayMode?: PlayerDisplayMode;
  onPictureInPictureChange?: (change: PlayerPictureInPictureChange) => void;
  autoEnterPictureInPicture?: boolean;
  onPlaybackStateChange?: (state: PlayerPlaybackStateChange) => void;
  onPlaybackTransportReady?: (transport: PlayerPlaybackTransport | null) => void;
  /** Called each time a newly loaded transport shows its first frame. */
  onFirstFrame?: () => void;
  onReturnFromPostRoll?: () => void;
  onRealtimeEvent?: (event: PlaybackRealtimeEventEnvelope) => void;
  onRealtimeConnectionStateChange?: (state: "disconnected" | "connecting" | "connected") => void;
  watchTogetherRoomId?: string | null;
  watchTogetherConnection?: WatchTogetherRoomConnectionResult;
}

const EXIT_PROGRESS_FLUSH_TIMEOUT_MS = 1_000;
const FIREFOX_COMPATIBILITY_FALLBACK_DELAY_MS = 8_000;
// How often a rejected autoplay is retried, and how many times. A transport
// swap tears the previous source down with `load()`, and the media element load
// algorithm is required to reject any play that is still pending with an
// AbortError — so the first attempt against a replacement transport can fail
// for a reason that is gone a moment later. The retry is timed rather than
// purely event-driven because the failure leaves nothing to wake it: once the
// engine has filled its buffer it stops fetching, so no further readiness event
// arrives. The budget is small so a genuinely blocked autoplay settles into a
// paused player with working controls instead of retrying forever.
const AUTOPLAY_RETRY_DELAY_MS = 400;
// Mouse clicks on the video surface wait this long for a second click
// (fullscreen toggle) before toggling play/pause.
const DOUBLE_CLICK_WINDOW_MS = 250;
const MAX_AUTOPLAY_ATTEMPTS = 4;
// A viewer who reports this many sustained stalls to a room within the window
// cannot keep up at its current quality and is offered a lower one.
const ROOM_STALLS_BEFORE_LOWER_QUALITY = 2;
const ROOM_STALL_WINDOW_MS = 5 * 60_000;
const LOWER_QUALITY_ACTION_LABEL = "Lower quality";
const PLAYBACK_NOTICE_VISIBLE_MS = 8_000;
const ROOM_RECONNECTING_MESSAGE = "Reconnecting to room. Controls are temporarily unavailable.";
// The server ends room sockets on a fixed lifetime and the client reconnects
// in well under a second, so only a longer gap is worth a warning.
const ROOM_RECONNECT_NOTICE_DELAY_MS = 2_000;

interface PlaybackNoticeState {
  title?: string;
  message: string;
  tone: "info" | "warning";
  actionLabel?: string;
  onAction?: () => void;
}

function watchTogetherNotice(
  message: string,
  tone: "info" | "warning",
  onAction?: () => void,
  actionLabel = "Join playback",
): PlaybackNoticeState {
  return {
    title: "Watch Party",
    message,
    tone,
    actionLabel: onAction ? actionLabel : undefined,
    onAction,
  };
}

function isAutoplayPolicyRejection(error: unknown): boolean {
  return error instanceof DOMException
    ? error.name === "NotAllowedError"
    : typeof error === "object" &&
        error !== null &&
        (error as { name?: unknown }).name === "NotAllowedError";
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
  onPlanInvalidated,
  onReanchorSeek,
  preferredSubtitleLanguage,
  preferredSubtitleTrackSignature,
  subtitleMode,
  showForcedSubtitles,
  profileLanguage,
  intro,
  introSkipMode = "ask",
  credits,
  recap = null,
  autoSkipRecap = false,
  preview = null,
  markerSegments,
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
  seekIntervals,
  onPictureInPictureChange,
  autoEnterPictureInPicture = false,
  onPlaybackStateChange,
  onPlaybackTransportReady,
  onFirstFrame,
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
  const autoSkippedRecapKeyRef = useRef<string | null>(null);
  const lastInputWasKeyboardRef = useRef(false);
  const endedFiredRef = useRef(false);
  const [hasEnded, setHasEnded] = useState(false);
  const onEndedRef = useRef(onEnded);
  const currentTimeRef = useRef(0);
  const durationRef = useRef(propDuration ?? 0);
  const compatibilityFallbackKeyRef = useRef<string | null>(null);
  const lastRoomCommandIdRef = useRef<string | null>(null);
  const appliedRoomCommandIdRef = useRef<string | null>(null);
  const roomCommandTimerRef = useRef<number | null>(null);
  // The advancing room position a playbackRate catch-up is converging toward,
  // when one is active. Non-null means playbackRate is intentionally not 1.
  const roomCatchupTargetRef = useRef<RoomCatchupTarget | null>(null);
  // Bounds media reloads that room corrections trigger; see roomSyncCatchup.ts.
  const roomReloadBudgetRef = useRef(createRoomReloadBudget());
  // Sustained stalls reported to the room, for offering a lower quality.
  const roomStallTimesRef = useRef<number[]>([]);
  const lowerQualityOfferedRef = useRef(false);
  const performPlayerSeekRef = useRef<(seconds: number) => boolean | Promise<boolean>>(() => false);
  const reportRoomReadyRef = useRef<() => { ok: boolean }>(() => ({ ok: false }));

  // Playback state
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [pendingSeekTime, setPendingSeekTime] = useState<number | null>(null);
  // Mirror for synchronous readers (relative skips): a seek whose target the
  // element has not reached yet — including a reanchor still being replanned —
  // is the position playback is heading to, so the next skip starts there.
  const pendingSeekTimeRef = useRef<number | null>(null);
  useEffect(() => {
    pendingSeekTimeRef.current = pendingSeekTime;
  }, [pendingSeekTime]);
  const [duration, setDuration] = useState(propDuration ?? 0);
  const [buffered, setBuffered] = useState<TimeRanges | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [videoFit, setVideoFit] = useState<VideoFitMode>("contain");
  const [buffering, setBuffering] = useState(false);
  const bufferingTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [awaitingFirstFrame, setAwaitingFirstFrame] = useState(true);
  const [isLeaving, setIsLeaving] = useState(false);
  const leaveInProgressRef = useRef(false);
  const [notice, setNotice] = useState<PlaybackNoticeState | null>(null);
  const noticeRef = useRef(notice);
  useEffect(() => {
    noticeRef.current = notice;
  }, [notice]);

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
  // Realtime callbacks can run before React commits the latest state. Keep the
  // selected job identity synchronous so batches from an older job cannot enter it.
  const liveTranslationIdentityRef = useRef<{ jobId: number; trackKey: string } | null>(null);
  const acceptedSubtitleJobRef = useRef<string | null>(null);
  const reportedSubtitleFailureRef = useRef<string | null>(null);
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
    liveTranslationIdentityRef.current = null;
    acceptedSubtitleJobRef.current = null;
    reportedSubtitleFailureRef.current = null;
    setPendingTranslationHandoff(null);
    setLiveTranslation(null);
    setLiveCues([]);
    setTranslationBuffering(false);
    translationPauseRef.current = false;
    return () => {
      acceptedSubtitleJobRef.current = null;
    };
  }, [activeFileId, sessionId]);

  const reportSubtitleFailure = useCallback((jobId: string, message?: string) => {
    if (reportedSubtitleFailureRef.current === jobId) return;
    reportedSubtitleFailureRef.current = jobId;
    toast.error(message ? `Subtitle processing failed: ${message}` : "Subtitle processing failed");
  }, []);

  const handleSubtitleJobAccepted = useCallback(
    (jobId: string) => {
      acceptedSubtitleJobRef.current = jobId;
      // Source loading can fail before Started, even before the POST response.
      // Reconcile once after acceptance so that early terminal event is not lost.
      void playerV2(playerConfig, "GET /api/v2/subtitles/ai/jobs/{job_id}", {
        path: { job_id: jobId },
      })
        .then((result) => {
          if (acceptedSubtitleJobRef.current === jobId && result?.job.status === "failed") {
            reportSubtitleFailure(jobId, result.job.error_message);
          }
        })
        .catch(() => {
          /* Realtime continues to report the accepted job. */
        });
    },
    [playerConfig, reportSubtitleFailure],
  );

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
  useEffect(() => {
    const video = videoRef.current;
    if (!video || !isPlayerReady) return;
    // The URL is available before HLS attaches and clears native cue lists.
    // Rebuild only once the actual source has loaded, including first startup.
    const handleLoadedMetadata = () => setSubtitleStreamGeneration((generation) => generation + 1);
    video.addEventListener("loadedmetadata", handleLoadedMetadata);
    return () => video.removeEventListener("loadedmetadata", handleLoadedMetadata);
  }, [isPlayerReady, planRevision]);

  const isFirefoxBrowser =
    typeof navigator !== "undefined" && isFirefoxUserAgent(navigator.userAgent);
  const watchTogether =
    watchTogetherConnection ??
    ({
      connectionState: "disconnected",
      room: null,
      suggestions: [],
      closedReason: null,
      replacementReason: null,
      rejoinRoom: () => {},
      transportCommand: null,
      serverTimeOffsetMs: 0,
      sendRoomMessage: () => ({ ok: false }),
      updatePolicy: async () => null,
      selectItem: async () => null,
      fallbackSource: async () => null,
      stageItem: async () => null,
      startPlayback: async () => null,
      stopPlayback: async () => null,
      updateSelectionMode: async () => null,
      setLobbyReady: () => ({ ok: false }),
      closeRoom: async () => {},
      createSuggestion: async () => {},
      deleteSuggestion: async () => {},
      vote: async () => {},
      unvote: async () => {},
      promoteSuggestion: async () => null,
    } satisfies WatchTogetherRoomConnectionResult);
  const connectionReplacedRef = useRef(false);
  connectionReplacedRef.current = Boolean(watchTogether.replacementReason);
  const [roomStallSignal, setRoomStallSignal] = useState(0);
  const noteRoomStall = useCallback(() => {
    // Once the lower quality is offered, stall history has done its job.
    if (lowerQualityOfferedRef.current) return;
    roomStallTimesRef.current.push(Date.now());
    setRoomStallSignal((signal) => signal + 1);
  }, []);
  const watchTogetherSync = useWatchTogetherPlaybackSync({
    roomConnection: watchTogether,
    sessionId,
    videoRef,
    streamOriginRef: timelineOffsetRef,
    appliedCommandIdRef: appliedRoomCommandIdRef,
    onSustainedStall: noteRoomStall,
  });
  const roomPlaybackActive = !!watchTogetherRoomId && !watchTogether.closedReason;
  const roomSyncWaiting = watchTogether.room?.playback_state === "waiting";
  // Media events acknowledge readiness while the room waits and while this
  // viewer recovers from a stall the room did not wait for.
  const roomReadinessPending = roomSyncWaiting || watchTogetherSync.catchingUp;
  const watchTogetherRoomActive = watchTogether.room !== null;

  const showWatchTogetherNotice = useCallback((...args: Parameters<typeof watchTogetherNotice>) => {
    setNotice(watchTogetherNotice(...args));
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
    setVideoFit("contain");
  }, [sessionId]);

  const roomConnected = watchTogether.connectionState === "connected";
  const roomReconnecting =
    !!watchTogetherRoomId &&
    !watchTogether.closedReason &&
    !watchTogether.replacementReason &&
    !roomConnected;
  const holdReconnectNotice = roomReconnecting && notice?.message === ROOM_RECONNECTING_MESSAGE;
  // Set once an outage outlasts the delay, so a notice that expires during it
  // hands back to the reconnect warning.
  const roomReconnectWarningDueRef = useRef(false);
  useEffect(() => {
    if (!watchTogetherRoomId || watchTogether.closedReason) {
      return;
    }
    if (watchTogether.replacementReason) {
      videoRef.current?.pause();
      setNotice(null);
      return;
    }
    if (roomConnected) {
      setNotice((current) => (current?.message === ROOM_RECONNECTING_MESSAGE ? null : current));
      return;
    }

    // A notice raised during the delay, such as an admin message, is newer
    // than the outage and keeps its place.
    const noticeAtDisconnect = noticeRef.current;
    const timer = setTimeout(() => {
      roomReconnectWarningDueRef.current = true;
      setNotice((current) =>
        current === null || current === noticeAtDisconnect
          ? watchTogetherNotice(ROOM_RECONNECTING_MESSAGE, "warning")
          : current,
      );
    }, ROOM_RECONNECT_NOTICE_DELAY_MS);
    return () => {
      clearTimeout(timer);
      roomReconnectWarningDueRef.current = false;
    };
  }, [
    roomConnected,
    watchTogether.closedReason,
    watchTogether.replacementReason,
    watchTogetherRoomId,
  ]);

  // Expire the notice from state rather than only hiding it, so the next
  // identical notice renders again. A minimized player keeps it until the
  // viewer can see it, and the reconnect warning stays for the whole outage.
  useEffect(() => {
    if (!notice || isDetached) return;
    if (holdReconnectNotice) return;
    const timer = setTimeout(
      () =>
        setNotice((current) => {
          if (current !== notice) return current;
          return roomReconnectWarningDueRef.current
            ? watchTogetherNotice(ROOM_RECONNECTING_MESSAGE, "warning")
            : null;
        }),
      PLAYBACK_NOTICE_VISIBLE_MS,
    );
    return () => clearTimeout(timer);
  }, [holdReconnectNotice, isDetached, notice]);

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

    // A refused reanchor never reaches its target. Resume relative skips from
    // the surviving stream instead of extending the abandoned request.
    pendingSeekTimeRef.current = null;
    setPendingSeekTime(null);
    const video = videoRef.current;
    if (video) setCurrentTime(toMediaTime(video.currentTime, timelineOffsetRef.current));

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

  const rememberPendingSeek = useCallback((seconds: number) => {
    pendingSeekTimeRef.current = seconds;
    setPendingSeekTime(seconds);
  }, []);

  // A refused reanchor never reaches its target. Resume relative skips from
  // the surviving stream unless a newer seek has already replaced the target.
  const releasePendingSeek = useCallback((seconds: number) => {
    if (pendingSeekTimeRef.current !== seconds) return;
    pendingSeekTimeRef.current = null;
    setPendingSeekTime(null);
    const video = videoRef.current;
    if (video) setCurrentTime(toMediaTime(video.currentTime, timelineOffsetRef.current));
  }, []);

  // Ends an active room catch-up nudge; any seek or local stop returns the
  // element to 1x so a stale rate never survives a context change.
  const resetRoomCatchupRate = useCallback(() => {
    roomCatchupTargetRef.current = null;
    const video = videoRef.current;
    if (video && video.playbackRate !== 1) {
      video.playbackRate = 1;
    }
  }, []);

  // Reports whether the seek was taken up, which callers that show an
  // affordance for it (the intro prompt) need in order to know whether the
  // affordance did anything.
  const performPlayerSeek = useCallback(
    (seconds: number): boolean | Promise<boolean> => {
      const video = videoRef.current;
      if (!video) return false;

      resetRoomCatchupRate();

      const nativeSeconds = toPlayerTime(seconds, timelineOffsetRef.current);
      if (canSeekAnywhere || isNativePositionInRanges(video.seekable, nativeSeconds)) {
        rememberPendingSeek(seconds);
        setCurrentTime(seconds);
        if (isHlsStream) video.currentTime = nativeSeconds;
        else handleSeek(nativeSeconds);
        return true;
      }

      // Outside the server-anchored window: this is a timeline operation, not
      // a failure, so it asks for a reanchor rather than reporting a failure.
      // The replan decides whether the seek took: a refused or failed replan
      // leaves playback where it was. With no handler the seek is dropped.
      if (!onReanchorSeek) return false;
      rememberPendingSeek(seconds);
      setCurrentTime(seconds);
      const accepted = onReanchorSeek(seconds);
      if (typeof accepted === "boolean") {
        if (!accepted) releasePendingSeek(seconds);
        return accepted;
      }
      return accepted.then(
        (ok) => {
          if (!ok) releasePendingSeek(seconds);
          return ok;
        },
        (error: unknown) => {
          releasePendingSeek(seconds);
          throw error;
        },
      );
    },
    [
      canSeekAnywhere,
      handleSeek,
      isHlsStream,
      onReanchorSeek,
      releasePendingSeek,
      rememberPendingSeek,
      resetRoomCatchupRate,
    ],
  );

  const handlePlayerSeek = useCallback(
    (seconds: number): boolean | Promise<boolean> => {
      if (watchTogether.replacementReason) return false;
      if (
        watchTogetherRoomId &&
        !watchTogether.closedReason &&
        (watchTogether.connectionState !== "connected" || !watchTogether.room)
      ) {
        showWatchTogetherNotice(ROOM_RECONNECTING_MESSAGE, "warning");
        return false;
      }
      if (watchTogether.room && !watchTogether.room.self_can_manage_room) {
        showWatchTogetherNotice("Only the host can seek the room.", "warning");
        return false;
      }
      if (watchTogether.room && watchTogetherSync.attachedSessionId !== sessionId) {
        showWatchTogetherNotice("Joining room playback. Try again in a moment.", "info");
        return false;
      }

      if (watchTogether.room) {
        const video = videoRef.current;
        const result = watchTogetherSync.requestTransport("seek", seconds, video?.paused ?? true);
        if (result.ok) {
          // Hold the requested position in the controls immediately. The media
          // element still waits for the room's scheduled transport command, and
          // a relative skip before then extends this target.
          rememberPendingSeek(seconds);
          setCurrentTime(seconds);
        }
        return result.ok;
      }
      return performPlayerSeek(seconds);
    },
    [
      performPlayerSeek,
      rememberPendingSeek,
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

  // Every relative skip starts on the canonical media timeline, including
  // remux playback whose HTML media clock starts at a nonzero offset. A seek
  // still in flight (a reanchor being replanned, a remux reload) is the origin
  // rather than the element clock, so a skip right after a scrub extends the
  // scrub instead of snapping back to where the element still is.
  const handleSkip = useCallback(
    (direction: "back" | "forward") => {
      const now =
        pendingSeekTimeRef.current ??
        (videoRef.current
          ? toMediaTime(videoRef.current.currentTime, timelineOffsetRef.current)
          : currentTimeRef.current);
      const seconds = direction === "back" ? seekIntervals.back : seekIntervals.forward;
      handlePlayerSeek(
        skipTarget(now, durationRef.current, direction === "back" ? -seconds : seconds),
      );
    },
    [handlePlayerSeek, seekIntervals.back, seekIntervals.forward],
  );
  const handleSkipRef = useRef(handleSkip);
  useEffect(() => {
    handleSkipRef.current = handleSkip;
  }, [handleSkip]);
  // One stable pair for every consumer that registers listeners (keyboard,
  // Media Session, controls), so a time update never re-registers anything.
  const skipActions = useMemo(
    () => ({
      back: () => handleSkipRef.current("back"),
      forward: () => handleSkipRef.current("forward"),
    }),
    [],
  );

  useMediaSkipHandlers(displayMode !== "postroll", skipActions.back, skipActions.forward);

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
        // The room is gone; the hub is where a new one starts.
        await onExit({
          ...exitState,
          destinationHref: "/rooms",
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

  // The host stopped playback: the room is still open, in the lobby, so
  // everyone goes back to the room page rather than the hub. A room that was
  // never playing (a stale lobby snapshot on first connect) is not a stop.
  const wasRoomPlayingRef = useRef(false);
  useEffect(() => {
    const phase = watchTogether.room?.phase;
    if (
      !watchTogetherRoomId ||
      watchTogether.closedReason ||
      watchTogether.replacementReason ||
      !phase
    )
      return;
    if (phase === "playing") {
      wasRoomPlayingRef.current = true;
      return;
    }
    if (!wasRoomPlayingRef.current || leaveInProgressRef.current) return;
    wasRoomPlayingRef.current = false;
    leaveInProgressRef.current = true;
    setIsLeaving(true);
    showWatchTogetherNotice("The host stopped playback.", "info");
    const exitState = buildExitState();
    void (async () => {
      try {
        await Promise.race([
          flushWatchProgress(),
          new Promise<void>((resolve) => {
            window.setTimeout(resolve, EXIT_PROGRESS_FLUSH_TIMEOUT_MS);
          }),
        ]);
      } catch {
        // Best effort, as on any other exit.
      }
      await onExit(exitState);
    })();
  }, [
    buildExitState,
    flushWatchProgress,
    onExit,
    showWatchTogetherNotice,
    watchTogether.closedReason,
    watchTogether.replacementReason,
    watchTogether.room?.phase,
    watchTogetherRoomId,
  ]);

  const handleLeave = useCallback(
    async (action: "exit" | "minimize") => {
      if (leaveInProgressRef.current) return;

      leaveInProgressRef.current = true;
      setIsLeaving(true);

      const exitState = buildExitState();
      if (watchTogether.replacementReason) exitState.destinationHref = "/rooms";

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
        // Leaving the player in a room returns everyone, host included, to the
        // room page (WatchPlaybackChrome navigates there when no destination
        // is given). Ending the party is the room page's decision, not a side
        // effect of closing the player.
        if (action === "minimize" && onMinimize && !watchTogether.replacementReason) {
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
    [buildExitState, flushWatchProgress, onExit, onMinimize, resetLeaveState, watchTogether],
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
      // Subtitle job events from another file or session are stale and ignored;
      // cue and terminal events must also belong to the translation on screen.
      const isForActiveStream = (payload: { file_id: number; session_id: string }) =>
        payload.file_id === activeFileId && payload.session_id === sessionId;
      const matchesLiveTranslation = (payload: { job_id: number; track_key: string }) => {
        const identity = liveTranslationIdentityRef.current;
        return identity?.jobId === payload.job_id && identity.trackKey === payload.track_key;
      };
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
          const payload = event.payload;
          if (!isForActiveStream(payload) || matchesLiveTranslation(payload)) break;
          acceptedSubtitleJobRef.current = String(payload.job_id);
          liveTranslationIdentityRef.current = {
            jobId: payload.job_id,
            trackKey: payload.track_key,
          };
          setPendingTranslationHandoff(null);
          // Remember the real selection we're displacing and whether we were
          // playing, so completion/failure can restore the right state.
          const wasPlaying =
            !(videoRef.current?.paused ?? true) ||
            (translationPauseRef.current && translationResumeOnFinishRef.current);
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
          if (!isForActiveStream(event.payload) || !matchesLiveTranslation(event.payload)) break;
          const cues = event.payload.cues.map((c) => ({
            start: c.start,
            end: c.end,
            text: c.text,
          }));
          setLiveCues((prev) => [...prev, ...cues]);
          break;
        }
        case "subtitle_translation_completed": {
          if (!isForActiveStream(event.payload) || !matchesLiveTranslation(event.payload)) break;
          liveTranslationIdentityRef.current = null;
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
          if (!isForActiveStream(event.payload)) break;
          const jobId = String(event.payload.job_id);
          if (!matchesLiveTranslation(event.payload)) {
            if (acceptedSubtitleJobRef.current === jobId)
              reportSubtitleFailure(jobId, event.payload.message);
            break;
          }
          liveTranslationIdentityRef.current = null;
          resumeFromTranslationPause();
          setLiveTranslation(null);
          setLiveCues([]);
          // Restore the selection the translation displaced rather than leaving
          // subtitles off.
          const restore = preTranslationSubtitleIndexRef.current;
          setActiveSubtitleIndex((idx) => (idx === LIVE_SUBTITLE_INDEX ? restore : idx));
          reportSubtitleFailure(jobId, event.payload.message);
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
      reportSubtitleFailure,
      sessionId,
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

  const onFirstFrameRef = useRef(onFirstFrame);
  useEffect(() => {
    onFirstFrameRef.current = onFirstFrame;
  }, [onFirstFrame]);

  // Every transport starts out awaiting its first frame, and the flag clears
  // on the event that proves a frame is on screen: `playing`, a `timeupdate`,
  // the seek that lands the start position, or a deliberately paused start.
  // The loading overlay goes with it, so this is when the viewer sees video.
  useEffect(() => {
    if (!awaitingFirstFrame) onFirstFrameRef.current?.();
  }, [awaitingFirstFrame]);

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
    (contentId: string, trigger: PlaybackStartTrigger) => {
      onNavigateEpisode?.(contentId, trigger);
    },
    [onNavigateEpisode],
  );

  const savedMarkerRegions = useMemo(
    () => resolveMarkerRegions({ intro, credits, recap, preview, marker_segments: markerSegments }),
    [intro, credits, recap, preview, markerSegments],
  );
  const activeIntro = markerOccurrenceAtTime(savedMarkerRegions, "intro", currentTime);
  const activeRecap = markerOccurrenceAtTime(savedMarkerRegions, "recap", currentTime);
  const activeCredits = markerOccurrenceAtTime(savedMarkerRegions, "credits", currentTime);
  const activePreview = markerOccurrenceAtTime(savedMarkerRegions, "preview", currentTime);
  const autoplayMarker = useMemo(() => {
    if (markerSegments !== undefined) {
      return resolveAutoplayMarker(savedMarkerRegions, duration, autoPlayNextPreview);
    }
    return autoPlayNextPreview && preview ? preview : credits;
  }, [markerSegments, savedMarkerRegions, duration, autoPlayNextPreview, preview, credits]);
  const nextEpisode = useNextEpisode(
    roomPlaybackActive ? null : autoplayMarker,
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
    if (prevEpisodeRef) handleNavigate(prevEpisodeRef.contentId, "viewer");
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

  // -- Intro/recap skip --
  const activeSkipMarker = [activeRecap, activeCredits, activePreview].find(
    (region) => region !== null && currentTime >= region.start && currentTime < region.end,
  );
  const skipMarker = useCallback(() => {
    if (activeSkipMarker) handlePlayerSeek(activeSkipMarker.end);
  }, [activeSkipMarker, handlePlayerSeek]);

  const introPromptCanSeek =
    !roomPlaybackActive ||
    (watchTogether.room?.self_can_manage_room === true &&
      watchTogetherSync.attachedSessionId === sessionId);
  const introKey = activeIntro
    ? `${sessionId}:${activeFileId ?? selectedVersion?.file_id ?? "unknown"}:${activeIntro.start}:${activeIntro.end}`
    : null;
  const {
    prompt: activeIntroPrompt,
    select: selectActiveIntroPrompt,
    dismiss: dismissActiveIntroPrompt,
  } = useIntroSkipPrompt({
    mode: introSkipMode ?? "ask",
    intro: activeIntro,
    introKey,
    currentTime,
    playing,
    enabled:
      displayMode === "foreground" &&
      isPlayerReady &&
      !awaitingFirstFrame &&
      introPromptCanSeek &&
      // An unknown mode neither prompts nor skips. The mode above is only the
      // placeholder that keeps the hook's contract total while this is false.
      introSkipMode !== null,
    onSeek: handlePlayerSeek,
  });

  useEffect(() => {
    if (!autoSkipRecap || !activeRecap || !isPlayerReady || awaitingFirstFrame) {
      return;
    }
    if (currentTime < activeRecap.start || currentTime >= activeRecap.end) {
      return;
    }
    if (
      roomPlaybackActive &&
      (!watchTogether.room?.self_can_manage_room ||
        watchTogetherSync.attachedSessionId !== sessionId)
    ) {
      return;
    }

    const recapKey = `${sessionId}:${activeFileId ?? "unknown"}:${activeRecap.start}:${activeRecap.end}`;
    if (autoSkippedRecapKeyRef.current === recapKey) {
      return;
    }
    autoSkippedRecapKeyRef.current = recapKey;
    handlePlayerSeek(activeRecap.end);
  }, [
    activeFileId,
    autoSkipRecap,
    awaitingFirstFrame,
    currentTime,
    handlePlayerSeek,
    isPlayerReady,
    activeRecap,
    roomPlaybackActive,
    sessionId,
    watchTogether.room?.self_can_manage_room,
    watchTogetherSync.attachedSessionId,
  ]);

  // Only the bitrate matters for buffer sizing, and the plan states what is
  // actually being delivered rather than what the source file happens to hold.
  const plannedBitrateKbps = plan.effective_recipe.bitrate_kbps ?? 0;
  const plannedDynamicRange = plan.effective_recipe.dynamic_range;

  // -- hls.js lifecycle --
  useEffect(() => {
    const video = videoRef.current;
    if (!video || !isPlayerReady || hlsStartupGuardRef.current?.hasFailed()) return;

    let hls: HlsType | null = null;
    let destroyed = false;
    let playbackStarted = false;
    let autoplayInFlight = false;
    let autoplayAttempts = 0;
    let autoplayRetryTimer: ReturnType<typeof setTimeout> | null = null;
    let nativeHLSMetadataHandler: (() => void) | null = null;

    mediaRecoveryAttemptsRef.current = 0;
    setError(null);
    setAwaitingFirstFrame(true);

    const clearAutoplayRetry = () => {
      if (autoplayRetryTimer === null) return;
      clearTimeout(autoplayRetryTimer);
      autoplayRetryTimer = null;
    };

    const cleanupStartupListeners = () => {
      clearAutoplayRetry();
      video.removeEventListener("loadeddata", attemptAutoplayWhenReady);
      video.removeEventListener("canplay", attemptAutoplayWhenReady);
      video.removeEventListener("loadedmetadata", attemptAutoplayWhenReady);
      if (nativeHLSMetadataHandler) {
        video.removeEventListener("loadedmetadata", nativeHLSMetadataHandler);
        nativeHLSMetadataHandler = null;
      }
    };

    // Settles the player into a deliberate paused state: the startup guard is
    // told playback is viable so it does not report a bogus startup timeout,
    // and the first frame is shown with the controls up.
    const settlePaused = () => {
      playbackStarted = true;
      cleanupStartupListeners();
      hlsStartupGuardRef.current?.markPlaybackStarted();
      setAwaitingFirstFrame(false);
      setPlaying(false);
    };

    const attemptAutoplayWhenReady = () => {
      if (destroyed || playbackStarted || autoplayInFlight) return;
      if (hlsStartupGuardRef.current?.hasFailed()) return;
      if (connectionReplacedRef.current) {
        settlePaused();
        return;
      }
      // HAVE_FUTURE_DATA means the browser has enough media to advance beyond
      // the current frame. Starting earlier can produce a visible first-frame
      // freeze where audio advances before video begins moving.
      if (video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA) return;
      if (!shouldAutoPlay) {
        settlePaused();
        return;
      }

      clearAutoplayRetry();
      autoplayInFlight = true;
      autoplayAttempts += 1;
      video.play().then(
        () => {
          autoplayInFlight = false;
          if (destroyed) return;
          if (connectionReplacedRef.current) {
            video.pause();
            settlePaused();
            return;
          }
          playbackStarted = true;
          cleanupStartupListeners();
        },
        (error: unknown) => {
          autoplayInFlight = false;
          if (destroyed) return;
          if (connectionReplacedRef.current) {
            settlePaused();
            return;
          }
          // The element is paused now, whatever happens next, so the transport
          // reflects that immediately.
          setPlaying(false);
          if (autoplayAttempts < MAX_AUTOPLAY_ATTEMPTS) {
            // Deliberately keeps the readiness listeners armed: whichever
            // wakes first — a later `canplay` or this timer — retries.
            autoplayRetryTimer = setTimeout(() => {
              autoplayRetryTimer = null;
              attemptAutoplayWhenReady();
            }, AUTOPLAY_RETRY_DELAY_MS);
            return;
          }
          // Out of retries. The engine has media buffered and simply is not
          // allowed to start it, so stop pretending startup is still in
          // progress and leave the viewer a player they can press play on.
          console.warn("[player] playback did not resume after the transport changed", error);
          settlePaused();
        },
      );
    };

    video.addEventListener("loadeddata", attemptAutoplayWhenReady);
    video.addEventListener("canplay", attemptAutoplayWhenReady);

    const attachNativeHLS = () => {
      video.src = effectiveStreamUrl;
      nativeHLSMetadataHandler = () => {
        video.currentTime = effectiveInitialPosition;
        attemptAutoplayWhenReady();
      };
      video.addEventListener("loadedmetadata", nativeHLSMetadataHandler, { once: true });
    };

    async function init() {
      if (!video || destroyed) return;

      if (isHlsStream) {
        try {
          const nativeSupported = video.canPlayType("application/vnd.apple.mpegurl") !== "";
          // Safari's HLS capability evidence comes from its media element, so
          // keep every Safari plan on that same engine. Chromium can also
          // advertise native HLS, but treats an in-progress copy remux as live
          // and jumps toward its rapidly advancing production edge; its
          // conservative HLS claims and runtime both use hls.js instead.
          const preferNativeHLS =
            typeof navigator !== "undefined" && isSafariBrowserV3(navigator.userAgent);
          const resolution = await resolveHLSEngineV3(
            plannedDynamicRange,
            nativeSupported,
            loadHLSJS,
            (error) => {
              console.error("[hls.js] Failed to initialize, falling back to native HLS:", error);
            },
            preferNativeHLS,
          );
          if (destroyed || hlsStartupGuardRef.current?.hasFailed()) return;

          if (resolution.engine === "native") {
            attachNativeHLS();
          } else if (resolution.engine === "hlsjs") {
            const Hls = resolution.hlsjs;
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
        // Direct play — set video src directly. Starting playback goes through
        // the same readiness gate as HLS rather than calling play() against a
        // src that has not loaded yet: a play issued at HAVE_NOTHING is racing
        // the load algorithm that is about to seek to the resume position, and
        // the spec has that algorithm reject it.
        video.src = effectiveStreamUrl;
        video.currentTime = effectiveInitialPosition;
        attemptAutoplayWhenReady();
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
    plannedDynamicRange,
    reportCurrentPlanFailure,
    shouldAutoPlay,
  ]);

  // -- Video event listeners --
  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;

    const onPlay = () => {
      if (connectionReplacedRef.current) {
        video.pause();
        return;
      }
      setPlaying(true);
    };
    const onPause = () => {
      resetRoomCatchupRate();
      setPlaying(false);
    };
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
    // `timeupdate` and `seeked` prove a frame is on screen only once the
    // loaded source has data for the current position. Tearing a transport
    // down (`removeAttribute("src")` and `load()`) resets the position and
    // queues a `timeupdate` at HAVE_NOTHING. That task can run after the next
    // transport sets `awaitingFirstFrame` but before React renders it, so
    // counting it would cancel the render: no loading overlay, a startup
    // guard marked started, and the new transport's first frame never seen.
    const hasCurrentFrame = () => video.readyState >= HTMLMediaElement.HAVE_CURRENT_DATA;
    const onTimeUpdate = () => {
      const nextTime = toMediaTime(video.currentTime, timelineOffsetRef.current);
      const resolved = resolvePendingSeekTime(nextTime, pendingSeekTime);
      setCurrentTime(resolved.currentTime);
      if (resolved.pendingSeekTime !== pendingSeekTime) {
        setPendingSeekTime(resolved.pendingSeekTime);
      }
      // A rate-based room catch-up that reached the advancing room position
      // returns to 1x.
      const catchupTarget = roomCatchupTargetRef.current;
      if (catchupTarget !== null && roomCatchupConverged(catchupTarget, nextTime, Date.now())) {
        roomCatchupTargetRef.current = null;
        video.playbackRate = 1;
        settleRoomReloads(roomReloadBudgetRef.current);
      }
      // A correction's reloaded media is playing. How long it took to load
      // becomes the lead for the next reload.
      if (
        !video.paused &&
        !video.seeking &&
        video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA &&
        roomReloadLanded(roomReloadBudgetRef.current, nextTime)
      ) {
        landRoomReload(roomReloadBudgetRef.current, Date.now());
      }
      // timeupdate is the most reliable signal that frames are rendering.
      // Also clears any stale buffering state from HLS segment transitions
      // where `waiting` fired but `canplay`/`playing` never followed.
      if (hasCurrentFrame()) markPlaybackStarted();
      clearBuffering();
      if (roomReadinessPending && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportReady();
      }
    };
    const onSeeked = () => {
      const resolved = resolvePendingSeekTime(
        toMediaTime(video.currentTime, timelineOffsetRef.current),
        pendingSeekTime,
      );
      setCurrentTime(resolved.currentTime);
      setPendingSeekTime(resolved.pendingSeekTime);
      // Reloading a stream can finish an older native seek. It does not settle
      // the requested seek. Readiness is still evaluated: the sync hook checks
      // the actual media position against the room's seek target, and the
      // host may be accepted short of it.
      if (roomReadinessPending && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportReady();
      }
      if (resolved.pendingSeekTime !== null) return;
      if (hasCurrentFrame()) markPlaybackStarted();
      clearBuffering();
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
      if (roomReadinessPending && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportReady();
      }
    };
    const onPlaying = () => {
      clearBuffering();
      markPlaybackStarted();
      if (roomReadinessPending && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportReady();
      }
    };
    const onLoadedData = () => {
      if (roomReadinessPending && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportReady();
      }
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
    video.addEventListener("canplaythrough", onCanPlay);
    video.addEventListener("loadeddata", onLoadedData);
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
      video.removeEventListener("canplaythrough", onCanPlay);
      video.removeEventListener("loadeddata", onLoadedData);
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
    resetRoomCatchupRate,
    roomReadinessPending,
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
  const isCoarsePointer = useCoarsePointer();
  const hideTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const surfaceTapTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Inverse of the play/pause a fired single-click timer applied, so a
  // browser-recognized second click (event.detail === 2) can undo it.
  const singleClickRevertRef = useRef<"play" | "pause" | null>(null);

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

  useEffect(() => {
    const markKeyboardInput = (event: KeyboardEvent) => {
      if (
        event.key === "Tab" ||
        event.key === "Enter" ||
        event.key === " " ||
        event.key.startsWith("Arrow")
      ) {
        lastInputWasKeyboardRef.current = true;
      }
    };
    const markPointerInput = () => {
      lastInputWasKeyboardRef.current = false;
    };
    document.addEventListener("keydown", markKeyboardInput, true);
    document.addEventListener("pointerdown", markPointerInput, true);
    return () => {
      document.removeEventListener("keydown", markKeyboardInput, true);
      document.removeEventListener("pointerdown", markPointerInput, true);
    };
  }, []);

  const focusTransportAfterIntroPrompt = useCallback(() => {
    resetControlsTimer();
    window.setTimeout(() => {
      containerRef.current
        ?.querySelector<HTMLElement>('[role="slider"][aria-label="Seek"]')
        ?.focus({ preventScroll: true });
    }, 0);
  }, [resetControlsTimer]);

  const selectIntroPrompt = useCallback(() => {
    if (!selectActiveIntroPrompt()) return false;
    focusTransportAfterIntroPrompt();
    return true;
  }, [focusTransportAfterIntroPrompt, selectActiveIntroPrompt]);

  const dismissIntroPrompt = useCallback(() => {
    if (!dismissActiveIntroPrompt()) return false;
    focusTransportAfterIntroPrompt();
    return true;
  }, [dismissActiveIntroPrompt, focusTransportAfterIntroPrompt]);

  const introPromptVisible = activeIntroPrompt !== null;
  useEffect(() => {
    if (!introPromptVisible || displayMode !== "foreground") return;

    const promptSelector = '[data-intro-skip-prompt="true"]';

    const handlePromptKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented) return;
      const target = event.target instanceof HTMLElement ? event.target : null;
      const targetInPrompt = target?.closest(promptSelector) != null;
      const isEditingOrInMenu =
        !targetInPrompt &&
        target?.closest(
          'input, textarea, select, [contenteditable="true"], [role="dialog"], [role="menu"], [role="listbox"]',
        ) != null;
      if (isEditingOrInMenu) return;

      const isSelect = event.key === "Enter" || event.key === " ";
      const isBack = event.key === "Escape" || event.key === "BrowserBack";
      if (!isSelect && !isBack) return;

      // Enter and Space belong to whatever control has focus: taking them at
      // the document would make Space on Play/Pause or on the seek slider skip
      // the intro and swallow the press. So Select is only claimed when the
      // pill itself is focused, or when no control is — the case the spec's
      // "handled at the player root" rule exists for. Back stays global: it has
      // no competing meaning on the transport, and it must reach the pill
      // whether or not the pill is in the focus tree.
      const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      const promptHasFocus = targetInPrompt || active?.closest(promptSelector) != null;
      const focusIsUnclaimed =
        active === null || active === document.body || active === containerRef.current;
      if (isSelect && !promptHasFocus && !focusIsUnclaimed) return;

      const handled = isSelect ? selectIntroPrompt() : dismissIntroPrompt();
      if (!handled) return;
      event.preventDefault();
      event.stopImmediatePropagation();
    };

    document.addEventListener("keydown", handlePromptKeyDown, true);
    return () => document.removeEventListener("keydown", handlePromptKeyDown, true);
  }, [dismissIntroPrompt, displayMode, introPromptVisible, selectIntroPrompt]);

  const focusIntroPromptOnMount = (() => {
    if (!activeIntroPrompt || !lastInputWasKeyboardRef.current) return false;
    const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const interactionInProgress =
      active?.closest(
        'input, textarea, select, [contenteditable="true"], [role="dialog"], [role="menu"], [role="listbox"], [role="slider"]',
      ) !== null ||
      containerRef.current?.querySelector('[role="dialog"], [role="menu"], [role="listbox"]') !=
        null;
    return !interactionInProgress;
  })();

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
    if (!markerEditor.editing) return savedMarkerRegions;
    const source = markerEditor.draft;
    const out: MarkerRegionView[] = [];
    for (const kind of MARKER_KINDS) {
      const range = source[kind];
      if (range && range.end > range.start) {
        out.push({ kind, start: range.start, end: range.end });
      }
    }
    return out;
  }, [markerEditor.editing, markerEditor.draft, savedMarkerRegions]);

  // -- Playback info overlay --
  const [showPlaybackInfo, setShowPlaybackInfo] = useState(false);

  // -- Fullscreen tracking --
  useEffect(() => {
    const video = videoRef.current as
      | (HTMLVideoElement & {
          webkitDisplayingFullscreen?: boolean;
        })
      | null;

    const onChange = () => {
      const isDocFullscreen = !!document.fullscreenElement;
      const isVideoFullscreen = !!video?.webkitDisplayingFullscreen;
      setIsFullscreen(isDocFullscreen || isVideoFullscreen);
    };

    document.addEventListener("fullscreenchange", onChange);
    video?.addEventListener("webkitbeginfullscreen", onChange);
    video?.addEventListener("webkitendfullscreen", onChange);

    return () => {
      document.removeEventListener("fullscreenchange", onChange);
      video?.removeEventListener("webkitbeginfullscreen", onChange);
      video?.removeEventListener("webkitendfullscreen", onChange);
    };
  }, []);

  // -- Subtitle appearance --
  const { settings: subtitleSettings, containerStyle, cueStyle } = useSubtitleAppearance();
  const {
    positionStyle: subtitlePositionStyle,
    fontScale: subtitleFontScale,
    coverCrop,
  } = useSubtitleLayout(containerRef, videoRef, subtitleSettings.position, videoFit);
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
  const [textSubtitleState, setTextSubtitleState] = useState("idle");
  const [assSubtitleState, setASSSubtitleState] = useState("idle");
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
    setTextSubtitleState,
  );

  // -- ASS/SSA subtitle rendering via JASSUB (client-side libass) --
  const { isActive: isASSActive } = useASSSubtitles(
    videoRef,
    subtitleUrls,
    activeSubtitleIndex,
    isDetached,
    timelineOffsetSeconds,
    subtitleDelayMs,
    setASSSubtitleState,
    videoFit,
    coverCrop,
  );
  const subtitleLoadState = isASSActive ? assSubtitleState : textSubtitleState;

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
    // Live AI cues belong to the client overlay, not the server inventory.
    // Leave the current plan alone until a real downloaded track is ready.
    if (activeSubtitleIndex === LIVE_SUBTITLE_INDEX) {
      requestedSubtitleTrackChangeRef.current = null;
      return;
    }
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
  const setPlayback = useCallback(
    (action: "play" | "pause" | "toggle") => {
      if (watchTogether.replacementReason) return;
      const video = videoRef.current;
      if (!video) return;
      const shouldPlay = action === "toggle" ? video.paused : action === "play";
      if (
        watchTogetherRoomId &&
        !watchTogether.closedReason &&
        (watchTogether.connectionState !== "connected" || !watchTogether.room)
      ) {
        showWatchTogetherNotice(ROOM_RECONNECTING_MESSAGE, "warning");
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
          shouldPlay ? "play" : "pause",
          currentTimeRef.current,
          !shouldPlay,
        );
        return;
      }

      if (shouldPlay) {
        video.play().catch(() => {});
        return;
      }

      video.pause();
    },
    [sessionId, showWatchTogetherNotice, watchTogether, watchTogetherRoomId, watchTogetherSync],
  );

  // Zero-argument form for button and keyboard handlers, which pass the
  // click event as the first argument.
  const handlePlayPause = useCallback(() => setPlayback("toggle"), [setPlayback]);

  const handleFullscreenToggle = useCallback(() => {
    const video = videoRef.current as
      | (HTMLVideoElement & {
          webkitSupportsFullscreen?: boolean;
          webkitDisplayingFullscreen?: boolean;
          webkitEnterFullscreen?: () => void;
          webkitExitFullscreen?: () => void;
        })
      | null;

    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
    } else if (video?.webkitDisplayingFullscreen) {
      video.webkitExitFullscreen?.();
    } else if (containerRef.current?.requestFullscreen) {
      containerRef.current.requestFullscreen().catch(() => {
        if (
          video?.webkitSupportsFullscreen !== false &&
          typeof video?.webkitEnterFullscreen === "function"
        ) {
          video.webkitEnterFullscreen();
        }
      });
    } else if (
      video?.webkitSupportsFullscreen !== false &&
      typeof video?.webkitEnterFullscreen === "function"
    ) {
      video.webkitEnterFullscreen();
    }
  }, []);

  const handleSurfaceTap = useCallback(
    (event?: React.MouseEvent<HTMLElement>) => {
      if (!isCoarsePointer) {
        // Mouse: single click toggles play/pause, double click toggles
        // fullscreen. The browser's click count (event.detail) is the only
        // double-click signal, so the user's OS interval and positional
        // tolerance apply. Play/pause is deferred for a short window so a
        // fast double click doesn't pause and immediately resume before
        // entering fullscreen; a slower double click undoes the play/pause
        // that already fired by sending the explicit inverse action. Clicks
        // beyond the second in one sequence are ignored.
        const clickCount = event?.detail ?? 1;
        if (clickCount >= 3) return;
        if (clickCount === 2) {
          if (surfaceTapTimerRef.current) {
            clearTimeout(surfaceTapTimerRef.current);
            surfaceTapTimerRef.current = null;
          } else if (singleClickRevertRef.current) {
            setPlayback(singleClickRevertRef.current);
          }
          singleClickRevertRef.current = null;
          handleFullscreenToggle();
          return;
        }
        // A second single click (different spot, so the browser did not
        // count it as a double) restarts the window; one toggle results.
        if (surfaceTapTimerRef.current) clearTimeout(surfaceTapTimerRef.current);
        singleClickRevertRef.current = null;
        surfaceTapTimerRef.current = setTimeout(() => {
          surfaceTapTimerRef.current = null;
          const willPlay = videoRef.current?.paused ?? false;
          singleClickRevertRef.current = willPlay ? "pause" : "play";
          setPlayback(willPlay ? "play" : "pause");
        }, DOUBLE_CLICK_WINDOW_MS);
        return;
      }
      if (surfaceTapTimerRef.current) {
        clearTimeout(surfaceTapTimerRef.current);
        surfaceTapTimerRef.current = null;
        const rect = event?.currentTarget.getBoundingClientRect();
        const relativeX = rect && event ? (event.clientX - rect.left) / rect.width : 0.5;
        if (relativeX < 1 / 3) {
          handleSkip("back");
          resetControlsTimer();
        } else if (relativeX > 2 / 3) {
          handleSkip("forward");
          resetControlsTimer();
        } else {
          handlePlayPause();
        }
        return;
      }
      surfaceTapTimerRef.current = setTimeout(() => {
        surfaceTapTimerRef.current = null;
        if (controlsVisible) {
          clearControlsTimer();
          setControlsVisible(false);
        } else {
          resetControlsTimer();
        }
      }, 250);
    },
    [
      clearControlsTimer,
      controlsVisible,
      handleFullscreenToggle,
      handlePlayPause,
      handleSkip,
      isCoarsePointer,
      resetControlsTimer,
      setPlayback,
    ],
  );

  useEffect(
    () => () => {
      if (surfaceTapTimerRef.current) clearTimeout(surfaceTapTimerRef.current);
    },
    [],
  );

  useEffect(() => {
    reportRoomReadyRef.current = watchTogetherSync.reportReady;
  }, [watchTogetherSync.reportReady]);

  useEffect(() => {
    const command = watchTogether.transportCommand;
    const roomSelectionRevision = watchTogether.room?.selection_revision;
    if (
      !watchTogetherRoomId ||
      watchTogether.closedReason ||
      watchTogether.connectionState !== "connected" ||
      !command ||
      roomSelectionRevision === undefined ||
      roomSelectionRevision === null ||
      !sessionId
    ) {
      lastRoomCommandIdRef.current = null;
      appliedRoomCommandIdRef.current = null;
      return;
    }
    if (command.session_id && command.session_id !== sessionId) {
      lastRoomCommandIdRef.current = null;
      appliedRoomCommandIdRef.current = null;
      return;
    }
    if (command.selection_revision !== roomSelectionRevision) {
      lastRoomCommandIdRef.current = null;
      appliedRoomCommandIdRef.current = null;
      return;
    }
    if (command.command_id === lastRoomCommandIdRef.current) {
      return;
    }

    lastRoomCommandIdRef.current = command.command_id;
    appliedRoomCommandIdRef.current = null;

    if (roomCommandTimerRef.current !== null) {
      window.clearTimeout(roomCommandTimerRef.current);
      roomCommandTimerRef.current = null;
    }

    const serverExecuteAt = Date.parse(command.execute_at);
    const localExecuteAt = Number.isFinite(serverExecuteAt)
      ? serverExecuteAt - watchTogether.serverTimeOffsetMs
      : Date.now();
    const delay = Math.max(0, localExecuteAt - Date.now());

    const applyRoomPosition = (video: HTMLVideoElement) => {
      if (command.action === "pause" || command.action === "seek") {
        resetRoomCatchupRate();
      }
      const reloadBudget = roomReloadBudgetRef.current;
      if (command.action === "seek") {
        // An explicit room seek supersedes any correction reload.
        abandonRoomReload(reloadBudget, Date.now());
        settleRoomReloads(reloadBudget);
      }

      // Room corrections land here. Small drift against a target the element
      // cannot reach without a rebuild converges via playbackRate instead of
      // forcing a seek-reanchor replan; see roomSyncCatchup.ts.
      const catchupTarget: RoomCatchupTarget = {
        positionSeconds: command.position_seconds,
        executeAtMs: localExecuteAt,
      };
      // A late play command must choose its correction against the same
      // advancing position used to detect convergence.
      const targetPositionSeconds =
        command.action === "play"
          ? roomCatchupExpectedPosition(catchupTarget, Date.now())
          : command.position_seconds;
      const targetLocallySeekable =
        canSeekAnywhere ||
        isNativePositionInRanges(
          video.seekable,
          toPlayerTime(targetPositionSeconds, timelineOffsetRef.current),
        );
      const decision = decideRoomCatchup({
        action: command.action,
        targetPositionSeconds,
        localPositionSeconds: toMediaTime(video.currentTime, timelineOffsetRef.current),
        targetLocallySeekable,
      });

      const targetBuffered = isNativePositionInRanges(
        video.buffered,
        toPlayerTime(targetPositionSeconds, timelineOffsetRef.current),
      );
      if (decision.kind === "seek" && command.action === "play" && !targetBuffered) {
        // A correction to media that is not buffered yet loads it: a range
        // request or a stream rebuild. Keep playing behind the room while an
        // earlier reload is still loading or the backoff runs; the server
        // repeats corrections that still apply.
        const now = Date.now();
        if (!roomReloadAllowed(reloadBudget, now)) return;
        const reloadTarget = beginRoomReload(
          reloadBudget,
          targetPositionSeconds,
          now,
          mediaDurationSeconds(backendDurationRef.current, durationRef.current),
        );
        const reloadGeneration = reloadBudget.generation;
        // Only this reload's own seek counts as its load: an in-stream seek
        // that was taken, or an adopted reanchor. Other stream swaps, such as
        // a subtitle replan, cannot settle it.
        void Promise.resolve(performPlayerSeekRef.current(reloadTarget)).then((accepted) => {
          if (reloadBudget.generation !== reloadGeneration) return;
          if (accepted) noteRoomReloadLoading(reloadBudget);
          else abandonRoomReload(reloadBudget, Date.now());
        });
      } else if (decision.kind === "seek") {
        performPlayerSeekRef.current(targetPositionSeconds);
      } else if (decision.kind === "rate") {
        video.playbackRate = decision.rate;
        // The room keeps advancing at 1x from the command's execution, so
        // convergence tracks that moving position, not the static one.
        roomCatchupTargetRef.current = catchupTarget;
      } else {
        // Already at the room position; drop any stale convergence nudge.
        resetRoomCatchupRate();
        if (command.action === "play") settleRoomReloads(reloadBudget);
      }
    };

    roomCommandTimerRef.current = window.setTimeout(() => {
      roomCommandTimerRef.current = null;
      void (async () => {
        const video = videoRef.current;
        if (!video) {
          return;
        }

        appliedRoomCommandIdRef.current = command.command_id;
        applyRoomPosition(video);

        if (command.action === "pause" || command.action === "seek") {
          video.pause();
        }

        if (command.action === "play") {
          try {
            await video.play();
          } catch (error) {
            if (!isMountedRef.current || lastRoomCommandIdRef.current !== command.command_id)
              return;
            // Only an autoplay policy refusal needs a click. A transport swap
            // (subtitle or quality change) tears the source down with load(),
            // which aborts a pending play() for a reason that is gone a moment
            // later; the replacement transport's own startup resumes playback.
            if (!isAutoplayPolicyRejection(error)) {
              window.setTimeout(() => {
                if (!isMountedRef.current || lastRoomCommandIdRef.current !== command.command_id)
                  return;
                const currentVideo = videoRef.current;
                if (!currentVideo || !currentVideo.paused) return;
                void currentVideo.play().catch(() => {});
              }, AUTOPLAY_RETRY_DELAY_MS);
              return;
            }
            resetRoomCatchupRate();
            showWatchTogetherNotice(
              "Your browser blocked automatic playback. Click to join playback.",
              "warning",
              () => {
                if (!isMountedRef.current || lastRoomCommandIdRef.current !== command.command_id)
                  return;
                const currentVideo = videoRef.current;
                if (!currentVideo) return;
                applyRoomPosition(currentVideo);
                void currentVideo
                  .play()
                  .then(() => {
                    if (
                      !isMountedRef.current ||
                      lastRoomCommandIdRef.current !== command.command_id
                    )
                      return;
                    reportRoomReadyRef.current();
                  })
                  .catch(() => {
                    if (
                      !isMountedRef.current ||
                      lastRoomCommandIdRef.current !== command.command_id
                    )
                      return;
                    resetRoomCatchupRate();
                  });
              },
            );
          }
        }

        if (command.playback_state === "waiting") {
          reportRoomReadyRef.current();
        }
      })().catch(() => {});
    }, delay);

    return () => {
      if (roomCommandTimerRef.current !== null) {
        window.clearTimeout(roomCommandTimerRef.current);
        roomCommandTimerRef.current = null;
        // A clock or plan update can restart this effect before execution.
        // Keep the cancelled command eligible for its replacement timer.
        lastRoomCommandIdRef.current = null;
      }
    };
  }, [
    canSeekAnywhere,
    resetRoomCatchupRate,
    sessionId,
    watchTogetherRoomId,
    watchTogether.closedReason,
    watchTogether.connectionState,
    watchTogether.room?.selection_revision,
    watchTogether.serverTimeOffsetMs,
    watchTogether.transportCommand,
    showWatchTogetherNotice,
  ]);

  // A rate nudge outlives neither the room nor a connection gap: corrections
  // stop flowing while disconnected, so playback returns to 1x immediately.
  useEffect(() => {
    if (
      !watchTogetherRoomId ||
      watchTogether.closedReason ||
      watchTogether.connectionState !== "connected"
    ) {
      resetRoomCatchupRate();
      setPendingSeekTime(null);
      const video = videoRef.current;
      if (video) setCurrentTime(toMediaTime(video.currentTime, timelineOffsetRef.current));
    }
  }, [
    watchTogetherRoomId,
    watchTogether.closedReason,
    watchTogether.connectionState,
    resetRoomCatchupRate,
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

  // -- Keyboard shortcuts --
  useKeyboardShortcuts(
    videoRef,
    containerRef,
    handlePlayPause,
    skipActions,
    toggleCaptions,
    handleTogglePiP,
    displayMode === "foreground",
  );

  // The id is the plan's own quality label, handed back to the server verbatim.
  const handleQualitySelect = useCallback(
    (id: string) => {
      // A new quality replaces the stream; reload pacing from the old one
      // does not apply to it.
      roomReloadBudgetRef.current = createRoomReloadBudget();
      onQualitySelect?.(id, currentTime);
    },
    [currentTime, onQualitySelect],
  );

  const handlePlayPauseRef = useRef(handlePlayPause);
  const handlePlayerSeekRef = useRef(handlePlayerSeek);
  const handleTogglePiPRef = useRef(handleTogglePiP);
  const handleQualitySelectRef = useRef(handleQualitySelect);

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
    handleQualitySelectRef.current = handleQualitySelect;
  }, [handleQualitySelect]);

  // Rebuild pacing belongs to one stream in one room.
  useEffect(() => {
    roomReloadBudgetRef.current = createRoomReloadBudget();
  }, [sessionId, watchTogetherRoomId]);

  // Stall history is judged per quality: choosing another starts over, and an
  // offer computed for the previous quality no longer applies.
  useEffect(() => {
    roomStallTimesRef.current = [];
    lowerQualityOfferedRef.current = false;
    setNotice((current) => (current?.actionLabel === LOWER_QUALITY_ACTION_LABEL ? null : current));
  }, [activeQualityId, sessionId, watchTogetherRoomId]);

  const lowerQualityChoiceRef = useRef(() =>
    lowerQualityOption(qualityOptions, activeQualityId, plan.effective_recipe?.bitrate_kbps),
  );
  useEffect(() => {
    lowerQualityChoiceRef.current = () =>
      lowerQualityOption(qualityOptions, activeQualityId, plan.effective_recipe?.bitrate_kbps);
    // A replan can leave no lower rung; an offer that cannot act is withdrawn.
    if (!lowerQualityChoiceRef.current()) {
      setNotice((current) =>
        current?.actionLabel === LOWER_QUALITY_ACTION_LABEL ? null : current,
      );
    }
  }, [activeQualityId, plan.effective_recipe?.bitrate_kbps, qualityOptions]);

  // A viewer who keeps stalling in a room cannot keep up at this quality.
  // Offer one step down, once per quality; the room's shared source is kept.
  useEffect(() => {
    if (roomStallSignal === 0 || lowerQualityOfferedRef.current) return;
    const now = Date.now();
    const recent = roomStallTimesRef.current.filter((at) => now - at < ROOM_STALL_WINDOW_MS);
    roomStallTimesRef.current = recent;
    if (recent.length < ROOM_STALLS_BEFORE_LOWER_QUALITY) return;
    if (!lowerQualityOption(qualityOptions, activeQualityId, plan.effective_recipe?.bitrate_kbps)) {
      return;
    }
    lowerQualityOfferedRef.current = true;
    showWatchTogetherNotice(
      "Your connection is having trouble keeping up with the party.",
      "warning",
      () => {
        // A replan can change the ladder while the offer shows; choose the
        // step down from what is available when the viewer accepts.
        const lower = lowerQualityChoiceRef.current();
        if (lower) handleQualitySelectRef.current(lower.id);
      },
      LOWER_QUALITY_ACTION_LABEL,
    );
  }, [
    activeQualityId,
    plan.effective_recipe?.bitrate_kbps,
    qualityOptions,
    roomStallSignal,
    showWatchTogetherNotice,
  ]);

  // Explain a room that keeps playing without someone. This viewer hears it
  // once per stream; the spinner caption covers later stalls.
  const catchingUpExplainedRef = useRef(false);
  useEffect(() => {
    catchingUpExplainedRef.current = false;
  }, [sessionId, watchTogetherRoomId]);
  useEffect(() => {
    if (!watchTogetherSync.catchingUp || catchingUpExplainedRef.current) return;
    catchingUpExplainedRef.current = true;
    showWatchTogetherNotice(
      "The party kept playing. You'll rejoin at the current scene once your stream catches up.",
      "info",
    );
  }, [showWatchTogetherNotice, watchTogetherSync.catchingUp]);

  const previousRoomRef = useRef(watchTogether.room);
  useEffect(() => {
    const previous = previousRoomRef.current;
    previousRoomRef.current = watchTogether.room;
    const leftBehind = roomMembersLeftBehind(previous, watchTogether.room);
    if (leftBehind.length > 0) {
      showWatchTogetherNotice(
        `Continuing without ${leftBehind.join(", ")}. They'll catch up.`,
        "info",
      );
    }
  }, [showWatchTogetherNotice, watchTogether.room]);

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
      skipBack: skipActions.back,
      skipForward: skipActions.forward,
      seekTo: (seconds: number) => {
        handlePlayerSeekRef.current(seconds);
      },
      togglePictureInPicture: () => handleTogglePiPRef.current(),
    };

    onPlaybackTransportReady(transport);
    return () => onPlaybackTransportReady(null);
  }, [onPlaybackTransportReady, skipActions]);

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
        case "plan_invalidated": {
          // The server decided the route it planned cannot serve this source
          // after all. Ack (already sent by the transport), replan off it, and
          // report the outcome: a rejection is the server's cue to stop the
          // session so the client's own recovery can mint a fresh attempt.
          const invalidated = readPlanInvalidatedPayload(command.payload);
          if (!invalidated) {
            throw new Error("invalid_plan_invalidated_payload");
          }
          if (!onPlanInvalidated) {
            throw new Error("plan_invalidation_unsupported");
          }
          const replaced = await onPlanInvalidated(
            invalidated.plan_id,
            invalidated.reason,
            currentTimeRef.current,
          );
          if (!replaced) {
            throw new Error("plan_invalidation_replan_failed");
          }
          return;
        }
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
    [handleExit, onPlanInvalidated, performPlayerSeek],
  );

  const realtime = usePlaybackRealtime({
    sessionId,
    onCommand: executeRealtimeCommand,
    onEvent: handleRealtimeEvent,
    supportedCommands: VIDEO_PLAYBACK_COMMANDS,
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

  const handleStopRoomPlayback = useCallback(async () => {
    await stopWatchTogetherPlayback(watchTogether.stopPlayback);
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
          className={`absolute top-[max(1rem,env(safe-area-inset-top))] left-[max(1rem,env(safe-area-inset-left))] z-50 flex items-center gap-3 transition-opacity duration-300 ${
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

      {!isDetached &&
      watchTogetherRoomId &&
      !watchTogether.closedReason &&
      !watchTogether.replacementReason ? (
        <WatchTogetherPanel
          room={watchTogether.room}
          connectionState={watchTogether.connectionState}
          visible={controlsVisible}
          onCopyInvite={() => void handleCopyWatchTogetherInvite()}
          onToggleGuestControl={(policy) => void handleToggleGuestControl(policy)}
          onEndRoom={() => void handleEndRoom()}
          onStopPlayback={() => void handleStopRoomPlayback()}
        />
      ) : null}

      {!isDetached && watchTogetherRoomId && watchTogether.replacementReason ? (
        <div className="absolute inset-0 z-50 flex items-center justify-center bg-black/80 px-6">
          <div role="alert" className="max-w-sm text-center text-white">
            <h2 className="text-lg font-semibold">Watch Party joined on another device</h2>
            <p className="mt-2 text-sm text-white/70">{watchTogether.replacementReason}</p>
            <button
              type="button"
              onClick={watchTogether.rejoinRoom}
              className="mt-4 rounded-md bg-white px-4 py-2 text-sm font-medium text-black"
            >
              Rejoin Watch Party
            </button>
            <button
              type="button"
              onClick={() => void handleExit()}
              className="mt-4 ml-3 rounded-md border border-white/30 px-4 py-2 text-sm font-medium"
            >
              Leave Watch Party
            </button>
          </div>
        </div>
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
              {watchTogether.room?.members?.some((member) => member.is_syncing)
                ? `Waiting for ${watchTogether.room.members
                    .filter((member) => member.is_syncing)
                    .map((member) => (member.is_self ? "you" : member.display_name))
                    .join(", ")}`
                : "Waiting for everyone to be ready."}
            </div>
          </div>
        </div>
      )}

      {/* Buffering spinner (mid-playback stalls only) */}
      {!isDetached && buffering && !roomSyncWaiting && !awaitingFirstFrame && isPlayerReady && (
        <div
          role="status"
          aria-label={watchTogetherSync.catchingUp ? "Catching up to the party" : "Buffering"}
          className="pointer-events-none absolute inset-0 z-30 flex flex-col items-center justify-center gap-3"
        >
          <div className="h-10 w-10 animate-spin rounded-full border-2 border-white/20 border-t-white" />
          {watchTogetherSync.catchingUp ? (
            <span className="rounded-full bg-black/60 px-3 py-1 text-xs font-medium text-white/85">
              Catching up to the party
            </span>
          ) : (
            <span className="sr-only">Buffering</span>
          )}
        </div>
      )}

      {!isDetached && notice ? (
        <PlaybackNoticeOverlay
          title={notice.title}
          message={notice.message}
          tone={notice.tone}
          actionLabel={notice.actionLabel}
          onAction={notice.onAction}
        />
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
        className={`${isDetached ? "h-full w-full" : "absolute inset-0 h-full w-full"} ${
          videoFit === "cover" ? "object-cover" : "object-contain"
        }`}
        onClick={displayMode === "postroll" ? undefined : handleSurfaceTap}
        playsInline
        style={!isPlayerReady ? { visibility: "hidden" } : undefined}
      />

      {!isDetached &&
        activeSubtitleIndex !== null &&
        (subtitleLoadState === "loading" || subtitleLoadState === "error") && (
          <div
            role="status"
            className="pointer-events-none absolute inset-x-0 top-20 z-40 flex justify-center"
          >
            <span className="rounded bg-black/75 px-3 py-2 text-sm text-white">
              {subtitleLoadState === "loading"
                ? "Loading subtitles…"
                : "Subtitles couldn't load. Retrying…"}
            </span>
          </div>
        )}

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

      {/* Intro skip / undo prompt */}
      {!isDetached && activeIntroPrompt && (
        <IntroSkipButton
          onSkip={selectIntroPrompt}
          label={activeIntroPrompt.label}
          caption={activeIntroPrompt.caption}
          timer={activeIntroPrompt}
          controlsVisible={controlsVisible}
          focusOnMount={focusIntroPromptOnMount}
        />
      )}
      {!isDetached && !activeIntroPrompt && !nextEpisode.showCountdown && activeSkipMarker && (
        <IntroSkipButton
          onSkip={skipMarker}
          label={MARKER_SKIP_LABELS[activeSkipMarker.kind]}
          controlsVisible={controlsVisible}
        />
      )}

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
          skipSeconds={seekIntervals}
          onSkip={skipActions}
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
          preferredSubtitleLanguage={preferredSubtitleLanguage}
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
          onSubtitleJobAccepted={handleSubtitleJobAccepted}
          audioTracks={audioTracks}
          activeAudioIndex={activeAudioIndex}
          onAudioSelect={onAudioSelect}
          qualityOptions={qualityOptions}
          activeQualityId={activeQualityId}
          isTranscoding={replanning}
          qualityError={replanError}
          onQualitySelect={handleQualitySelect}
          versionLocked={!!watchTogetherRoomId}
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
            onSwitchVersion && !watchTogetherRoomId
              ? (fileId) => onSwitchVersion(fileId, currentTime)
              : undefined
          }
          onTogglePiP={handleTogglePiP}
          onPlayPause={handlePlayPause}
          onSeek={handlePlayerSeek}
          onVolumeChange={handleVolumeChange}
          onMutedChange={handleMutedChange}
          videoFit={videoFit}
          onVideoFitToggle={() =>
            setVideoFit((current) => (current === "cover" ? "contain" : "cover"))
          }
          onFullscreenToggle={handleFullscreenToggle}
          onSurfaceTap={handleSurfaceTap}
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
