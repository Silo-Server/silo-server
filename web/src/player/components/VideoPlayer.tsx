import { useMediaSkipHandlers } from "../hooks/useMediaSkipHandlers";
import {
  MEDIA_ENDED_EVENT,
  bufferedAheadMs,
  useAutoQualityDownshift,
} from "../hooks/useAutoQualityDownshift";
import { BUFFERING_QUALITY_COPY } from "../buffering-quality";
import type {
  BufferingAnswer,
  BufferingMode,
  BufferingPlan,
  BufferingReplan,
  BufferingSummary,
} from "../buffering-quality";
import { skipTarget } from "../utils/skipTarget";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { ParsedCue } from "../utils/parseVTT";
import { resolveSubtitleAutoSelect } from "../utils/subtitleSort";
import type HlsType from "hls.js";
import { PlayerControls } from "./PlayerControls";
import { PlaybackInfoOverlay } from "./PlaybackInfoOverlay";
import { PlaybackNoticeOverlay, PlaybackNoticeStack } from "./PlaybackNoticeOverlay";
import { IntroSkipButton } from "./IntroSkipButton";
import { MarkerReportButton } from "./MarkerReportButton";
import { BufferingQualityPill } from "./BufferingQualityPill";
import { MarkerEditPanel } from "./MarkerEditPanel";
import { NextEpisodeOverlay } from "./NextEpisodeOverlay";
import { usePlaybackRealtime } from "../hooks/usePlaybackRealtime";
import { useWatchProgress } from "../hooks/useWatchProgress";
import type { PlaybackConnectionStatus } from "../hooks/usePlaybackSession";
import { useKeyboardShortcuts } from "../hooks/useKeyboardShortcuts";
import { usePlayerFullscreenRoot } from "../context/PlayerFullscreenContext";
import { isNativeVideoFullscreen, isPlayerFullscreen, toggleFullscreen } from "../utils/fullscreen";
import { useIntroSkipPrompt } from "../hooks/useIntroSkipPrompt";
import { useRemuxSeeking } from "../hooks/useRemuxSeeking";
import { useSubtitleTracks } from "../hooks/useSubtitleTracks";
import { useASSSubtitles } from "../hooks/useASSSubtitles";
import { usePGSSubtitles } from "../hooks/usePGSSubtitles";
import type { EngineCapabilities } from "../engine/capabilities";
import { resolveOriginalPlaybackRoute, type NativeOriginalSupport } from "../engine/routing";
import type { BrowserEngineSession } from "../engine";
import { ENGINE_READ_FAILED, ENGINE_STARTUP_TIMEOUT } from "../engine/protocol";
import { useSubtitleSync } from "../hooks/useSubtitleSync";
import { useSubtitleSyncFeedback } from "../hooks/useSubtitleSyncFeedback";
import { syncKeyOf } from "../utils/subtitleSync";
import { SubtitleSyncIndicator } from "./SubtitleSyncIndicator";
import { useSubtitleAppearance } from "../hooks/useSubtitleAppearance";
import { useSubtitleDelay } from "../hooks/useSubtitleDelay";
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
import {
  remainingRoomHeldSeek,
  resolvePendingSeekTime,
  ROOM_HELD_SEEK_TIMEOUT_MS,
} from "../utils/pendingSeek";
import { resolveVersionAudioLanguage } from "../utils/effectiveAudioLanguage";
import { StartupGuard } from "../utils/startupGuard";
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
import type { PlayerTrickplay } from "../trickplay";
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
  createRoomSeekCorrector,
  decideRoomCommand,
  expireRoomReload,
  isNativePositionInRanges,
  landRoomReload,
  nextRoomNudgeRate,
  noteRoomCorrectorSeek,
  noteRoomReloadLoading,
  observeRoomCorrector,
  roomCorrectorSeekDue,
  roomCorrectorSeekTarget,
  roomPositionAt,
  roomReloadAllowed,
  roomReloadInFlight,
  roomSeekAlreadyPending,
  roomMembersLeftBehind,
  roomReloadLanded,
  roomSyncDeadbandSeconds,
  roomSyncLoopIntervalMs,
  roomSyncLoopSettleMs,
  settleRoomReloads,
  type RoomIssuedSeek,
  type RoomPositionReference,
} from "../utils/roomSyncCatchup";
import { pendingServerSubtitleSelection } from "../utils/playableSubtitles";
import {
  copyWatchTogetherInvite,
  endWatchTogetherRoom,
  stopWatchTogetherPlayback,
  setWatchTogetherGuestControl,
} from "@/lib/watchTogetherActions";
import { toast } from "sonner";
import { MediaDataMeter } from "../media-data-meter";
import { mediaElementReleased, sourceLoadStarted } from "../bandwidth-probe";
import { networkInformation, type NetworkInformationLike } from "../network-information";

function pillKey(
  event: KeyboardEvent,
  promptSelector: string,
  root: HTMLElement | null,
): "select" | "back" | null {
  const target = event.target instanceof HTMLElement ? event.target : null;
  const targetInPrompt = target?.closest(promptSelector) != null;
  const isEditingOrInMenu =
    !targetInPrompt &&
    target?.closest(
      'input, textarea, select, [contenteditable="true"], [role="dialog"], [role="menu"], [role="listbox"]',
    ) != null;
  if (isEditingOrInMenu) return null;

  const isSelect = event.key === "Enter" || event.key === " ";
  const isBack = event.key === "Escape" || event.key === "BrowserBack";
  if (!isSelect && !isBack) return null;

  // Select belongs to the focused control: Space on Play/Pause or Seek must
  // not activate this pill. Claim it only for pill focus or unclaimed focus
  // at the document/player root. Back remains global for the pill.
  const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  const promptHasFocus = targetInPrompt || active?.closest(promptSelector) != null;
  const focusIsUnclaimed = active === null || active === document.body || active === root;
  if (isSelect && !promptHasFocus && !focusIsUnclaimed) return null;
  return isSelect ? "select" : "back";
}

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
  /**
   * The browser decode engine, when browser decoding is on and this browser
   * can run it. An `original_http` plan the media element cannot play goes to
   * the engine instead.
   */
  browserEngine?: EngineCapabilities | null;
  /** What the media element itself plays from an untouched original. */
  nativeOriginalSupport?: NativeOriginalSupport;
  /** Whether a newly adopted transport should begin playing immediately. */
  shouldAutoPlay?: boolean;
  /** True while a replan is in flight, so the quality menu can show progress. */
  replanning?: boolean;
  /** True while a fresh start replaces the session, such as a version switch. */
  replacing?: boolean;
  /** Server-described replan error, if the last replan was refused. */
  replanError?: string | null;
  /** Title for the replan error, used when surfacing the refusal as a toast. */
  replanErrorTitle?: string | null;
  sessionId: string;
  bufferingTitleId?: string;
  selectedVersion?: PlayerFileVersion;
  versions?: PlayerFileVersion[];
  activeFileId?: number | null;
  chapters?: PlayerChapter[];
  /** Seek-bar previews of the file being played. */
  trickplay?: PlayerTrickplay | null;
  trickplayUpdatedAt?: number;
  /** A preview sheet failed to load; read the previews again. */
  onTrickplayError?: () => void;
  onSwitchVersion?: (
    fileId: number,
    currentPosition: number,
    onAdopt?: (planRevision?: number) => void,
  ) => void;
  subtitleUrls: PlayerSubtitleInfo[];
  initialPosition: number;
  /** `quality_change` replan for a label taken from `plan.available_qualities`. */
  onQualitySelect?: (
    label: string,
    currentPosition: number,
    buffering?: BufferingReplan,
    onAdopt?: (planRevision?: number, sent?: boolean) => void,
  ) => void;
  /** `track_change` replan for a subtitle the server has to render. */
  onSubtitleTrackChange?: (combinedIndex: number | null, currentPosition: number) => void;
  /** `failure_recovery` replan after the client could not play the plan. */
  onPlanFailure?: (failure: FailureV3, currentPosition: number) => void;
  /**
   * The stream already played and then lost its connection to the server. The
   * session reconnects and resumes at `positionSeconds`, playing again only
   * when `resume` is set.
   */
  onConnectionLost?: (positionSeconds: number, resume: boolean) => void;
  /** Starts another reconnect after the session gave up. */
  onRetryConnection?: () => void;
  /** The session's mid-stream connection state. */
  connectionStatus?: PlaybackConnectionStatus;
  /** Why reconnecting gave up, while `connectionStatus` is `lost`. */
  connectionErrorTitle?: string | null;
  connectionError?: string | null;
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
  intro: PlayerTimeRange | null;
  /**
   * null means the connected server's answer is not in yet, which is not the
   * same as "ask": prompting on a guess can skip an intro the viewer told the
   * server to leave alone.
   */
  introSkipMode?: IntroSkipMode | null;
  /** Show the marked range's confidence above the skip prompt. Off by default. */
  showMarkerConfidence?: boolean;
  /** Show the marked range's source above the skip prompt. Off by default. */
  showMarkerSource?: boolean;
  /**
   * The marker plugin installation serving the report route, present only when the
   * viewer has enabled reporting. Null draws no report control at all.
   */
  markerReportInstallationId?: number | null;
  /** The item's content id, which carries the ids a report is addressed by. */
  markerReportContentId?: string | null;
  /** Effective profile mode, seeded by the host before its settings read resolves. */
  bufferingMode?: BufferingMode;
  /**
   * The bitrate cap the session's replans send. It changes when the link
   * turns metered or unmetered and the other quality pair applies: a quality
   * change, so the controller starts over and drops its kept estimate.
   */
  bandwidthCapKbps?: number | null;
  onBufferingSummary?: (
    plan: BufferingPlan,
    summary: BufferingSummary,
    pageEnding?: boolean,
  ) => void;
  /** The playback attempt; a new start drops the kept step-down estimate. */
  playbackAttemptId?: string | null;
  /** The latest Auto lowering's estimate, for the session's own later Auto replans. */
  onKeptEstimate?: (kbps: number | undefined) => void;
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
// MediaError.MEDIA_ERR_NETWORK: the media was usable, then fetching it failed.
const MEDIA_ERR_NETWORK = 2;
const FIREFOX_COMPATIBILITY_FALLBACK_DELAY_MS = 8_000;
/**
 * Firefox's copy-remux stall is media that arrived and still made no frame.
 * With less than this buffered past the playhead the link is slow, which the
 * buffering controller handles, so the fallback waits.
 */
const FIREFOX_STALL_BUFFERED_MS = 2_000;

// A progressive remux is one forward-only response. A paused browser stops
// reading it, and the server closes it once it has made no progress for its
// stall window (180s by default, see internal/httpstream). The browser cannot
// resume it where it was: Chromium fetches the stream again from its origin and
// stalls while it re-reads everything up to its cursor; Firefox fails the
// stream or jumps back to the origin. Playing again after this long reopens the
// stream at the paused position instead. The value sits well inside the
// default window.
const PROGRESSIVE_REMUX_IDLE_REOPEN_MS = 60_000;
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
// Buffering this soon after a failed media request is treated as an outage,
// not a slow link, by the Auto downshift.
/** Where the element's buffered media ends, in seconds; 0 with none. */
function bufferedEndSeconds(media: HTMLMediaElement): number {
  const ranges = media.buffered;
  return ranges.length ? ranges.end(ranges.length - 1) : 0;
}
const PLAYBACK_NOTICE_VISIBLE_MS = 8_000;
/** The buffering controller's display-only notices show for 5 seconds on every client. */
const BUFFERING_NOTICE_VISIBLE_MS = 5_000;
const ROOM_RECONNECTING_MESSAGE = "Reconnecting to room. Controls are temporarily unavailable.";
// The server returns a room to the lobby once its position is within two
// seconds of the end of the file. A viewer this close to its own end when the
// room leaves playback saw the item finish, allowing for trailing the room.
const ROOM_ITEM_END_WINDOW_SECONDS = 5;
// The server ends room sockets on a fixed lifetime and the client reconnects
// in well under a second, so only a longer gap is worth a warning.
const ROOM_RECONNECT_NOTICE_DELAY_MS = 2_000;

interface PlaybackNoticeState {
  title?: string;
  message: string;
  tone: "info" | "warning";
  actionLabel?: string;
  onAction?: () => void;
  /** How long it shows; PLAYBACK_NOTICE_VISIBLE_MS when absent. */
  visibleMs?: number;
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
  browserEngine = null,
  nativeOriginalSupport,
  shouldAutoPlay = true,
  replanning = false,
  replacing = false,
  replanError = null,
  replanErrorTitle = null,
  sessionId,
  selectedVersion,
  versions = [],
  activeFileId,
  chapters = [],
  trickplay = null,
  trickplayUpdatedAt,
  onTrickplayError,
  onSwitchVersion,
  subtitleUrls,
  initialPosition,
  onQualitySelect,
  onSubtitleTrackChange,
  onPlanFailure,
  onConnectionLost,
  onRetryConnection,
  connectionStatus = "connected",
  connectionErrorTitle = null,
  connectionError = null,
  onPlanInvalidated,
  onReanchorSeek,
  preferredSubtitleLanguage,
  preferredSubtitleTrackSignature,
  subtitleMode,
  showForcedSubtitles,
  intro,
  introSkipMode = "ask",
  showMarkerConfidence = false,
  showMarkerSource = false,
  markerReportInstallationId = null,
  markerReportContentId = null,
  bufferingMode = "always",
  bandwidthCapKbps,
  bufferingTitleId,
  onBufferingSummary,
  playbackAttemptId,
  onKeptEstimate,
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
  // Box under the <video> that holds the browser decode engine's canvas.
  const engineSurfaceRef = useRef<HTMLDivElement>(null);
  const [browserEngineActive, setBrowserEngineActive] = useState(false);
  // Bumped whenever an engine transport starts or stops. Frame callbacks
  // registered on one engine end with it, so renderers that drive themselves
  // from requestVideoFrameCallback (JASSUB) rebuild on it.
  const [engineTransportRevision, setEngineTransportRevision] = useState(0);
  const fullscreenRootRef = usePlayerFullscreenRoot();
  const isMountedRef = useRef(true);
  const hlsRef = useRef<HlsType | null>(null);
  // The plan's media requests, where its transport can see their bytes:
  // hls.js fragments and the browser engine's reads. The media element's own
  // loads show none.
  const mediaMeterRef = useRef<
    { planRevision: number; meter: MediaDataMeter; segmented: boolean } | undefined
  >(undefined);
  const planRevisionForThroughputRef = useRef(planRevision);
  useEffect(() => {
    planRevisionForThroughputRef.current = planRevision;
  }, [planRevision]);
  // The plan during which the browser last changed networks: its summary
  // sends no throughput, since the server files a sample under the
  // connection of the request that delivers it.
  const networkChangedInRevisionRef = useRef<number | undefined>(undefined);
  useEffect(() => {
    const connection = networkInformation() as (NetworkInformationLike & EventTarget) | undefined;
    let type = connection?.type;
    const changed = () => {
      networkChangedInRevisionRef.current = planRevisionForThroughputRef.current;
    };
    // The browser also fires `change` when only its downlink estimate moves.
    const onConnectionChange = () => {
      if (connection?.type === type) return;
      type = connection?.type;
      changed();
    };
    window.addEventListener("online", changed);
    window.addEventListener("offline", changed);
    connection?.addEventListener?.("change", onConnectionChange);
    return () => {
      window.removeEventListener("online", changed);
      window.removeEventListener("offline", changed);
      connection?.removeEventListener?.("change", onConnectionChange);
    };
  }, []);
  const startupGuardRef = useRef<StartupGuard | null>(null);
  const mediaRecoveryAttemptsRef = useRef(0);
  const lastRecoveryRef = useRef(0);
  // Media requests are failing: hls.js reported a failed load, the element a
  // network error or `stalled`, or the device went offline or came back.
  // Buffering meanwhile is an outage, not a slow link, for the Auto
  // downshift. Only media data arriving again (a buffered end past the one at
  // the failure) or playback running ends it; no time limit does. A
  // `progress` alone is not enough, since the browser decoder fires one every
  // second whether data arrived or not.
  const mediaOutageRef = useRef(false);
  const outageBufferedEndRef = useRef(0);
  const reportedPlanFailureKeyRef = useRef<string | null>(null);
  // Whether the current transport has shown a frame. A network error after
  // that is a lost connection, not a route that cannot play. Reset whenever
  // the transport is rebuilt (see the hls.js lifecycle effect).
  const streamPlayedRef = useRef(false);
  // When the current transport last stopped playing, or null while it plays.
  // Only a transport that has opened, shown a frame, or settled paused counts.
  const transportIdleSinceRef = useRef<number | null>(null);
  const connectionStatusRef = useRef(connectionStatus);
  connectionStatusRef.current = connectionStatus;
  const transportFailedForPlanRevisionRef = useRef<number | null>(null);
  const timelineOffsetRef = useRef(0);
  // Where the direct-play engine's clock starts, in player seconds. Chrome keeps
  // a Matroska file's container start time (seconds into a TS remux), so its
  // currentTime runs ahead of the media timeline by that much.
  const [directPlayOriginSeconds, setDirectPlayOriginSeconds] = useState(0);
  const planTimelineOffsetRef = useRef(0);
  // True between loading an original file and learning its clock origin. A
  // seek in that window is held as pending: written to the element it would
  // become the default start position and be misread as the origin.
  const directPlayOriginPendingRef = useRef(false);
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
  // A room rate nudge is active: playbackRate is intentionally off the base
  // rate. Only command execution and the drift loop start one.
  const roomNudgeActiveRef = useRef(false);
  // The room reference: the position described by the latest applied
  // transport command for the current selection. The drift loop measures
  // against it; snapshots never move it.
  const roomReferenceRef = useRef<RoomPositionReference | null>(null);
  // Starts a room member's player once its media is playable: true when the
  // room's latest applied command has it playing and its position is now
  // applied, so startup may play. The room effect keeps it current.
  const roomStartRef = useRef<((video: HTMLVideoElement) => boolean) | null>(null);
  // The current transport has not started or settled paused yet, so its
  // startup will still run once the media is playable.
  const transportStartupPendingRef = useRef(false);
  // Offers a click to join when the browser refuses to start a room member's
  // player for want of one; the room effect keeps it current.
  const offerRoomJoinRef = useRef<(() => void) | null>(null);
  // When the latest room command executed or finished executing. The drift
  // loop leaves playback alone for a moment after a command.
  // Local intervals in room sync (this settle, reload staleness and pacing,
  // seek spacing) run on the monotonic clock, so a wall-clock jump cannot
  // freeze them. Date.now() is used only where server time is involved.
  const roomCommandFinishedAtRef = useRef(Number.NEGATIVE_INFINITY);
  // The last seek room sync issued and when. Cleared once no seek is pending
  // and when the stream is replaced, so only an unfinished room seek in this
  // stream suppresses a repeat of itself.
  const roomIssuedSeekRef = useRef<RoomIssuedSeek | null>(null);
  // Seek-based drift correction for a player without rate nudges: the lead
  // its seeks aim ahead by and the wait between them; see RoomSeekCorrector.
  const roomSeekCorrectorRef = useRef(createRoomSeekCorrector());
  // The rate playback returns to when no room catch-up is active: the
  // viewer's chosen speed, or 1x while a Watch Party room owns the rate.
  const baseRateRef = useRef(1);
  // Bounds media reloads that room corrections trigger; see roomSyncCatchup.ts.
  const roomReloadBudgetRef = useRef(createRoomReloadBudget());
  // Sustained stalls reported to the room, for offering a lower quality.
  const roomStallTimesRef = useRef<number[]>([]);
  const lowerQualityOfferedRef = useRef(false);
  const performPlayerSeekRef = useRef<(seconds: number) => boolean | Promise<boolean>>(() => false);
  const reopenIdleProgressiveRemuxRef = useRef<() => void>(() => {});
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
    if (pendingSeekTime === null) roomIssuedSeekRef.current = null;
  }, [pendingSeekTime]);
  const [duration, setDuration] = useState(propDuration ?? 0);
  const [buffered, setBuffered] = useState<TimeRanges | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [isFullscreen, setIsFullscreen] = useState(false);
  // iPhone Safari plays fullscreen in its own native view, which draws no
  // page overlays, so no pill or notice can be seen there.
  const [nativeVideoFullscreen, setNativeVideoFullscreen] = useState(false);
  const [pageHidden, setPageHidden] = useState(
    () => typeof document !== "undefined" && document.visibilityState === "hidden",
  );
  const playerUnseen = isDetached || pageHidden || nativeVideoFullscreen;
  // A buffering notice that arrived while another notice held the slot.
  const [queuedBufferingNotice, setQueuedBufferingNotice] = useState<PlaybackNoticeState | null>(
    null,
  );
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
  // Subtitle delay in ms; positive shows later. It is the profile's timing
  // offset, so a change here follows the profile to every device.
  const [subtitleDelayMs, setSubtitleDelayMs] = useSubtitleDelay();

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
  // A stored subtitle the server returned for a request it had already
  // finished, waiting to be selected once the inventory lists it.
  const [pendingStoredSubtitle, setPendingStoredSubtitle] = useState<{
    jobId: string;
    subtitleId: string;
    refreshed: boolean;
  } | null>(null);

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
    setPendingStoredSubtitle(null);
    setLiveTranslation(null);
    setLiveCues([]);
    setTranslationBuffering(false);
    translationPauseRef.current = false;
    return () => {
      acceptedSubtitleJobRef.current = null;
    };
  }, [activeFileId, sessionId]);

  // -- Subtitle sync --
  // A sync or timing change alters what a stored or sidecar track's unchanged
  // URL serves. Each observed change bumps that subtitle's cue revision, which
  // makes the subtitle hooks refetch the track instead of reusing cues.
  const subtitleSyncKeys = useMemo(
    () => subtitleUrls.map(syncKeyOf).filter((key): key is string => key !== null),
    [subtitleUrls],
  );
  const [syncCueRevisions, setSyncCueRevisions] = useState<Record<string, number>>({});
  const bumpSyncCueRevision = useCallback((key: string) => {
    setSyncCueRevisions((prev) => ({ ...prev, [key]: (prev[key] ?? 0) + 1 }));
  }, []);
  const subtitleSync = useSubtitleSync({
    playerConfig,
    mediaFileId: activeFileId ?? undefined,
    sessionId,
    syncKeys: subtitleSyncKeys,
    onTimingChanged: bumpSyncCueRevision,
  });
  const subtitleTimingChanged = subtitleSync.timingChanged;
  const subtitleSyncUpdated = subtitleSync.syncUpdated;
  const activeSyncKey = syncKeyOf(
    activeSubtitleIndex !== null
      ? subtitleUrls.find((track) => track.index === activeSubtitleIndex)
      : null,
  );
  const activeSubtitleCueRevision =
    activeSyncKey !== null ? (syncCueRevisions[activeSyncKey] ?? 0) : 0;

  const reportSubtitleFailure = useCallback((jobId: string, message?: string) => {
    if (reportedSubtitleFailureRef.current === jobId) return;
    reportedSubtitleFailureRef.current = jobId;
    toast.error(message ? `Subtitle processing failed: ${message}` : "Subtitle processing failed");
  }, []);

  const handleSubtitleJobAccepted = useCallback(
    (jobId: string, resultSubtitleId?: string) => {
      acceptedSubtitleJobRef.current = jobId;
      if (resultSubtitleId) {
        // An earlier identical request already produced this track, and the
        // job is finished, so no event will follow. A live translation still
        // running for an earlier request no longer owns the display.
        liveTranslationIdentityRef.current = null;
        setPendingTranslationHandoff(null);
        setPendingStoredSubtitle({ jobId, subtitleId: resultSubtitleId, refreshed: false });
        return;
      }
      setPendingStoredSubtitle(null);
      // Source loading can fail before Started, even before the POST response.
      // Reconcile once after acceptance so that early terminal event is not lost.
      void playerV2(playerConfig, "GET /api/v2/subtitles/ai/jobs/{job_id}", {
        path: { job_id: jobId },
      })
        .then((result) => {
          if (acceptedSubtitleJobRef.current !== jobId || !result) return;
          if (result.job.status === "failed") {
            reportSubtitleFailure(jobId, result.job.error_message);
          } else if (result.job.status === "completed" && result.job.result_subtitle_id) {
            // An earlier identical request already produced this track.
            setPendingStoredSubtitle({
              jobId,
              subtitleId: result.job.result_subtitle_id,
              refreshed: false,
            });
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
  // Where a replan resumes. Until this transport has played, the player's
  // position is the previous route's or 0, so it is where this one was
  // heading: the plan's start (a resume point) or a seek made meanwhile.
  const replanPosition = useCallback(
    () => (streamPlayedRef.current ? currentTimeRef.current : subtitleFetchAnchorRef.current),
    [],
  );
  const reportCurrentPlanFailure = useCallback(
    (failure: FailureV3): boolean => {
      if (!onPlanFailure) return false;
      // While the session reconnects, a dying transport says nothing about
      // its route; the reconnect replaces it anyway.
      if (connectionStatusRef.current !== "connected") return true;
      // The revision is part of the key: a reconnect can hand back the same
      // route under the same attempt key, and its transport can fail as well.
      const failureKey = `${sessionId}:${plan.plan_attempt_key}:${planRevision}`;
      if (reportedPlanFailureKeyRef.current === failureKey) return true;
      reportedPlanFailureKeyRef.current = failureKey;
      transportFailedForPlanRevisionRef.current = planRevision;
      setError(null);
      // A transport that never played, in a playing room, resumes where the
      // room is now.
      const roomReference = roomReferenceRef.current;
      const resumeAt =
        !streamPlayedRef.current && roomReference?.playing
          ? roomPositionAt(roomReference, Date.now(), serverTimeOffsetRef.current)
          : replanPosition();
      onPlanFailure(failure, resumeAt);
      return true;
    },
    [onPlanFailure, plan.plan_attempt_key, planRevision, replanPosition, sessionId],
  );

  useEffect(() => {
    transportFailedForPlanRevisionRef.current = null;
  }, [planRevision]);

  /**
   * Hands a network failure to the session's reconnect when the stream has
   * already played. Returns false before the first frame, where the failure
   * keeps going through route recovery: a stream that never started may be
   * one this route cannot deliver.
   */
  const reportConnectionLost = useCallback((): boolean => {
    const video = videoRef.current;
    if (!onConnectionLost || !video || !streamPlayedRef.current) return false;
    if (connectionStatusRef.current === "connected") {
      // A seek still in flight is where the viewer wants to be.
      const positionSeconds =
        pendingSeekTimeRef.current ?? toMediaTime(video.currentTime, timelineOffsetRef.current);
      onConnectionLost(positionSeconds, !video.paused);
    }
    return true;
  }, [onConnectionLost]);
  // The transport effect reads it through a ref so a new callback identity
  // never tears the transport down.
  const reportConnectionLostRef = useRef(reportConnectionLost);
  reportConnectionLostRef.current = reportConnectionLost;
  // A start that missed the player's own deadline goes to the buffering
  // controller, which lowers when Auto lowers on its own and otherwise calls
  // back to fail the plan. Set once the controller's hook has run below.
  const startDeadlinePassedRef = useRef<(fail: () => void) => void>((fail) => fail());

  // While the session reconnects, nothing plays: the element is paused (so
  // progress reports say so), and hls.js stops hammering the server. The
  // transport is rebuilt from the plan the reconnect adopts.
  useEffect(() => {
    if (connectionStatus === "connected") return;
    hlsRef.current?.stopLoad();
    videoRef.current?.pause();
    if (bufferingTimerRef.current) {
      clearTimeout(bufferingTimerRef.current);
      bufferingTimerRef.current = null;
    }
    setBuffering(false);
  }, [connectionStatus]);

  const failStartup = useCallback(
    (cause: "deadline" | "network") => {
      console.error("[player] Playback startup timed out or exhausted recovery attempts");

      const activeHls = hlsRef.current;
      hlsRef.current = null;
      activeHls?.destroy();

      const video = videoRef.current;
      if (video) {
        video.removeAttribute("src");
        video.load();
      }
      const fail = () => {
        if (
          !reportCurrentPlanFailure({
            classification: "startup_timeout",
            message:
              cause === "deadline"
                ? "Playback showed no frame before its startup deadline."
                : "HLS playback exhausted its startup recovery budget.",
          })
        ) {
          setError("Playback failed. The media could not be loaded.");
        }
      };
      // When Auto lowers on its own, a start that missed its deadline lowers, as
      // a long stall would.
      if (cause === "deadline") startDeadlinePassedRef.current(fail);
      else fail();
    },
    [reportCurrentPlanFailure],
  );

  // Every transport must show a frame by the startup deadline, the element's
  // own progressive loads included: a start that never plays otherwise sits
  // on its spinner with no failure path. The browser engine keeps its own
  // readiness deadline and stands this one down when it attaches.
  useEffect(() => {
    if (!isPlayerReady) return;

    const guard = new StartupGuard(failStartup);
    startupGuardRef.current = guard;

    return () => {
      guard.dispose();
      if (startupGuardRef.current === guard) {
        startupGuardRef.current = null;
      }
    };
  }, [failStartup, isPlayerReady, planRevision]);

  // The media's full runtime, from the plan and nowhere else: on an HLS copy
  // remux the engine reports only the length produced so far, so substituting it
  // would make the scrubber grow while the viewer watches.
  const backendDuration = plan.source.duration_seconds ?? propDuration ?? 0;
  backendDurationRef.current = backendDuration;
  const effectiveInitialPosition = plan.timeline.player_start_seconds;
  const canSeekAnywhere = plan.timeline.can_seek_anywhere;
  // The menu is the plan's. The session's preference decides whether Auto is
  // lit, since `auto` names no rung; otherwise the plan names the entry it
  // delivers.
  const activeQualityId = qualityPreference;
  const activeQuality = plan.active_quality;
  const deliveredBitrateKbps = plan.effective_recipe.bitrate_kbps;
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
  // A routine report names the applied command only once any seek it
  // started has landed; until then the element still plays the old stream.
  const roomReportCommandId = useCallback(() => {
    if (pendingSeekTimeRef.current !== null || videoRef.current?.seeking) return null;
    return appliedRoomCommandIdRef.current;
  }, []);
  const watchTogetherSync = useWatchTogetherPlaybackSync({
    roomConnection: watchTogether,
    sessionId,
    videoRef,
    streamOriginRef: timelineOffsetRef,
    appliedCommandIdRef: appliedRoomCommandIdRef,
    onSustainedStall: noteRoomStall,
    reportCommandId: roomReportCommandId,
  });
  const roomPlaybackActive = !!watchTogetherRoomId && !watchTogether.closedReason;
  // Read by the transport's startup: a room member's player starts when the
  // room says so, not when its media is ready.
  const roomPlaybackActiveRef = useRef(roomPlaybackActive);
  roomPlaybackActiveRef.current = roomPlaybackActive;
  // Read by timers that must use the newest clock estimate without restarting.
  const serverTimeOffsetRef = useRef(watchTogether.serverTimeOffsetMs);
  serverTimeOffsetRef.current = watchTogether.serverTimeOffsetMs;
  // Room sync corrects the browser decode engine by seeking. Its audio keeps
  // pitch at other rates too, but rate nudges have not been checked against
  // room drift on it, so it keeps the seek path it had when they could not.
  const roomRateCapableRef = useRef(!browserEngineActive);
  roomRateCapableRef.current = !browserEngineActive;
  // The viewer's speed for this player session. A room plays everyone at 1x
  // and nudges the rate itself to keep viewers together.
  const [playbackSpeed, setPlaybackSpeed] = useState(1);
  const baseRate = roomPlaybackActive ? 1 : playbackSpeed;
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
  const planTimelineOffset = plan.timeline.timeline_offset_seconds;
  planTimelineOffsetRef.current = planTimelineOffset;
  const playsOriginalFile = plan.delivery === "original_http";
  const timelineOffsetSeconds =
    planTimelineOffset - (playsOriginalFile ? directPlayOriginSeconds : 0);
  timelineOffsetRef.current = timelineOffsetSeconds;

  // Media-time position playback is heading to, for consumers that need a
  // position before the element has media loaded (when currentTime still
  // reads 0): an in-flight seek target, else the session's start position.
  subtitleFetchAnchorRef.current =
    pendingSeekTime ?? toMediaTime(effectiveInitialPosition, planTimelineOffset);

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
    setQueuedBufferingNotice(null);
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
    if (!notice || playerUnseen) return;
    if (holdReconnectNotice) return;
    const timer = setTimeout(
      () =>
        setNotice((current) => {
          if (current !== notice) return current;
          return roomReconnectWarningDueRef.current
            ? watchTogetherNotice(ROOM_RECONNECTING_MESSAGE, "warning")
            : null;
        }),
      notice.visibleMs ?? PLAYBACK_NOTICE_VISIBLE_MS,
    );
    return () => clearTimeout(timer);
  }, [holdReconnectNotice, notice, playerUnseen]);

  // A queued buffering notice takes the slot once it is free.
  useEffect(() => {
    if (notice || !queuedBufferingNotice) return;
    setNotice(queuedBufferingNotice);
    setQueuedBufferingNotice(null);
  }, [notice, queuedBufferingNotice]);

  useEffect(() => {
    const onVisibility = () => setPageHidden(document.visibilityState === "hidden");
    document.addEventListener("visibilitychange", onVisibility);
    return () => document.removeEventListener("visibilitychange", onVisibility);
  }, []);

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

    let timer: ReturnType<typeof setTimeout>;
    const check = () => {
      if (compatibilityFallbackKeyRef.current === fallbackKey) {
        return;
      }
      // Still waiting for data: a slow start, not a Firefox stall.
      const video = videoRef.current;
      if (video && bufferedAheadMs(video) < FIREFOX_STALL_BUFFERED_MS) {
        timer = setTimeout(check, 1_000);
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
    };
    timer = setTimeout(check, FIREFOX_COMPATIBILITY_FALLBACK_DELAY_MS);

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

    const failureKey = `${sessionId}:${plan.plan_attempt_key}:${planRevision}`;
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

  // Where the media is on the element's clock. While an original file's clock
  // origin is unknown the element has no position on the stream yet, so that
  // is where playback is heading.
  const elementMediaPosition = useCallback((): number | null => {
    const video = videoRef.current;
    if (!video) return null;
    if (directPlayOriginPendingRef.current) return subtitleFetchAnchorRef.current;
    return toMediaTime(video.currentTime, timelineOffsetRef.current);
  }, []);

  // Publishes the element's duration on the media timeline. A direct-play
  // engine's duration runs on its own clock, which starts at the origin, so
  // it waits until the origin is known.
  const applyElementDuration = useCallback((video: HTMLVideoElement) => {
    if (directPlayOriginPendingRef.current) return;
    if (!video.duration || !isFinite(video.duration)) return;
    const originSeconds = planTimelineOffsetRef.current - timelineOffsetRef.current;
    const mediaDuration = video.duration - originSeconds;
    // For HLS EVENT playlists still being written, the element reports the
    // length produced so far. The plan's source duration is the media's
    // full runtime, so it wins whenever the engine reports something short.
    if (backendDurationRef.current && mediaDuration < backendDurationRef.current) return;
    setDuration(mediaDuration);
  }, []);

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

  // This viewer's newest room seek request the room has not run yet: the
  // origin of the next relative skip and the position the controls show (see
  // remainingRoomHeldSeek). The element, its room reports and the markers
  // follow the room's seeks as they run.
  const [roomHeldSeek, setRoomHeldSeek] = useState<number | null>(null);
  const roomHeldSeekRef = useRef<number | null>(null);
  const roomHeldSeekTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Whether the room's seek for the held request has arrived. Any command that
  // runs after it, including one that replaced it before it ran, answers the
  // request.
  const roomHeldSeekAnsweredRef = useRef(false);
  const holdRoomSeek = useCallback(
    (seconds: number | null) => {
      if (roomHeldSeekTimerRef.current !== null) clearTimeout(roomHeldSeekTimerRef.current);
      roomHeldSeekTimerRef.current = null;
      roomHeldSeekAnsweredRef.current = false;
      roomHeldSeekRef.current = seconds;
      setRoomHeldSeek(seconds);
      if (seconds === null) return;
      // A request the room never ran (dropped while reconnecting, or folded
      // into a seek that already landed) puts the timeline back on the media.
      roomHeldSeekTimerRef.current = setTimeout(() => {
        roomHeldSeekTimerRef.current = null;
        roomHeldSeekRef.current = null;
        setRoomHeldSeek(null);
        releasePendingSeek(seconds);
      }, ROOM_HELD_SEEK_TIMEOUT_MS);
    },
    [releasePendingSeek],
  );
  // The room answered the held request without running a seek to it here: the
  // timeline goes back to the media, and so does the request's pending target.
  const abandonRoomHeldSeek = useCallback(() => {
    const seconds = roomHeldSeekRef.current;
    if (seconds === null) return;
    holdRoomSeek(null);
    releasePendingSeek(seconds);
  }, [holdRoomSeek, releasePendingSeek]);
  useEffect(
    () => () => {
      if (roomHeldSeekTimerRef.current !== null) clearTimeout(roomHeldSeekTimerRef.current);
    },
    [],
  );

  // Ends an active room catch-up nudge; any seek or local stop returns the
  // element to the base rate so a stale rate never survives a context change.
  const resetRoomCatchupRate = useCallback(() => {
    roomNudgeActiveRef.current = false;
    const video = videoRef.current;
    if (video && video.playbackRate !== baseRateRef.current) {
      video.playbackRate = baseRateRef.current;
    }
  }, []);

  // Runs before this commit's other effects, so a catch-up reset in them
  // already returns to the committed rate. defaultPlaybackRate carries the
  // speed across media reloads (quality switches, stream rebuilds), which
  // reset playbackRate to it.
  useLayoutEffect(() => {
    baseRateRef.current = baseRate;
    const video = videoRef.current;
    if (!video) return;
    video.defaultPlaybackRate = baseRate;
    if (!roomNudgeActiveRef.current) video.playbackRate = baseRate;
  }, [baseRate]);

  // Asks the server for a stream anchored at `seconds`. The replan decides
  // whether that took: a refused or failed replan leaves playback where it
  // was. With no handler it returns false.
  const reanchorAt = useCallback(
    (seconds: number): boolean | Promise<boolean> => {
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
    [onReanchorSeek, releasePendingSeek, rememberPendingSeek],
  );

  // Reports whether the seek was taken up, which callers that show an
  // affordance for it (the intro prompt) need in order to know whether the
  // affordance did anything.
  const performPlayerSeek = useCallback(
    (seconds: number): boolean | Promise<boolean> => {
      const video = videoRef.current;
      if (!video) return false;

      resetRoomCatchupRate();

      if (directPlayOriginPendingRef.current) {
        rememberPendingSeek(seconds);
        setCurrentTime(seconds);
        return true;
      }

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
      return reanchorAt(seconds);
    },
    [
      canSeekAnywhere,
      handleSeek,
      isHlsStream,
      reanchorAt,
      rememberPendingSeek,
      resetRoomCatchupRate,
    ],
  );

  // See PROGRESSIVE_REMUX_IDLE_REOPEN_MS. The element plays from its buffer
  // until the reopened stream replaces it. A room keeps its own transport. A
  // replan or fresh start already in flight replaces this stream when it lands
  // a plan; the reopen waits for it and runs only if it does not.
  const isProgressiveRemux = plan.delivery === "server_remux_progressive";
  const planChangeInFlight = replanning || replacing;
  const planChangeInFlightRef = useRef(planChangeInFlight);
  planChangeInFlightRef.current = planChangeInFlight;
  const planRevisionRef = useRef(planRevision);
  planRevisionRef.current = planRevision;
  // What to do once the plan change in flight settles without replacing the
  // stream, and the plan revision it would replace.
  const afterPlanChangeRef = useRef<{ revision: number; run: () => void } | null>(null);
  const reopenIdleProgressiveRemux = useCallback(() => {
    const video = videoRef.current;
    if (!video || !isProgressiveRemux || watchTogetherRoomId) return;
    if (connectionStatusRef.current !== "connected") return;
    // A seek inside the buffer that the element has not reached yet is where
    // the viewer wants to be.
    const position =
      pendingSeekTimeRef.current ?? toMediaTime(video.currentTime, timelineOffsetRef.current);
    // The session reads the play intent when a new plan lands, which can be
    // before the effect that reports this play has run. That includes the
    // plan of a change already in flight.
    onPlaybackStateChange?.({
      currentTime: position,
      duration: durationRef.current,
      playing: true,
    });
    const revision = planRevisionRef.current;
    if (planChangeInFlightRef.current) {
      afterPlanChangeRef.current = {
        revision,
        // A viewer who paused meanwhile is still on the idle stream; the next
        // play reopens it.
        run: () => {
          if (videoRef.current?.paused) transportIdleSinceRef.current = 0;
          else reopenIdleProgressiveRemuxRef.current();
        },
      };
      return;
    }
    // When the reopen does not take, the session may have expired during the
    // pause or the route may no longer be granted. The reconnect replans or
    // starts a new attempt at the same position, unless another plan already
    // replaced this stream or the viewer has left.
    const reconnect = () => {
      if (!isMountedRef.current) return;
      if (planRevisionRef.current !== revision) return;
      if (connectionStatusRef.current !== "connected") return;
      onConnectionLost?.(position, !video.paused);
    };
    // A change can still look in flight here: this reopen's own replan until
    // its answer renders, or a version switch that started meanwhile. The
    // reconnect waits for it to settle, so a switch that lands a plan
    // replaces the stream instead, and a failed reopen reconnects once rather
    // than reopening again.
    const fallBack = () => {
      if (planChangeInFlightRef.current) afterPlanChangeRef.current = { revision, run: reconnect };
      else reconnect();
    };
    const accepted = reanchorAt(position);
    if (accepted === false) fallBack();
    else if (accepted !== true) accepted.then((ok) => ok || fallBack(), fallBack);
  }, [
    isProgressiveRemux,
    onConnectionLost,
    onPlaybackStateChange,
    reanchorAt,
    watchTogetherRoomId,
  ]);
  reopenIdleProgressiveRemuxRef.current = reopenIdleProgressiveRemux;

  useEffect(() => {
    const waiting = afterPlanChangeRef.current;
    if (waiting === null || planChangeInFlight) return;
    afterPlanChangeRef.current = null;
    // A change that landed a plan replaced the stream, which is not idle.
    if (planRevision === waiting.revision) waiting.run();
  }, [planChangeInFlight, planRevision]);

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
          holdRoomSeek(seconds);
        }
        return result.ok;
      }
      return performPlayerSeek(seconds);
    },
    [
      holdRoomSeek,
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
  // scrub instead of snapping back to where the element still is. In a room,
  // the viewer's held request comes first: the room's seeks for earlier skips
  // in a burst move the element without answering it.
  const handleSkip = useCallback(
    (direction: "back" | "forward") => {
      const now =
        roomHeldSeekRef.current ??
        pendingSeekTimeRef.current ??
        elementMediaPosition() ??
        currentTimeRef.current;
      const seconds = direction === "back" ? seekIntervals.back : seekIntervals.forward;
      handlePlayerSeek(
        skipTarget(now, durationRef.current, direction === "back" ? -seconds : seconds),
      );
    },
    [elementMediaPosition, handlePlayerSeek, seekIntervals.back, seekIntervals.forward],
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
  const [progressSessionEnded, setProgressSessionEnded] = useState(false);
  const markSessionEnded = useCallback(() => setProgressSessionEnded(true), []);
  const flushWatchProgress = useWatchProgress(
    sessionId,
    videoRef,
    timelineOffsetRef,
    markSessionEnded,
  );

  const buildExitState = useCallback((): PlaybackExitState => {
    const positionSeconds =
      elementMediaPosition() ?? toMediaTime(currentTime, timelineOffsetRef.current);
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
  }, [activeFileId, currentTime, duration, elementMediaPosition, selectedVersion]);

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

  // The host stopped playback, or the item finished: the room is still open,
  // in the lobby, so everyone goes back to the room page rather than the hub.
  // A room that was never playing (a stale lobby snapshot on first connect)
  // is not a stop.
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
    const finished =
      durationRef.current > 0 &&
      durationRef.current - currentTimeRef.current <= ROOM_ITEM_END_WINDOW_SECONDS;
    showWatchTogetherNotice(finished ? "Playback finished." : "The host stopped playback.", "info");
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

  // The server ended the session for good (an administrator stopped it while
  // this player could not be told directly): leave instead of reconnecting.
  const sessionEndedHandledRef = useRef(false);
  useEffect(() => {
    if (connectionStatus !== "ended" && !progressSessionEnded) return;
    if (sessionEndedHandledRef.current) return;
    sessionEndedHandledRef.current = true;
    toast.info("An administrator ended this playback session.");
    void handleExit();
  }, [connectionStatus, handleExit, progressSessionEnded]);

  const handleMinimize = useCallback(async () => {
    await handleLeave("minimize");
  }, [handleLeave]);

  // -- Subtitle toggle callback --
  const toggleCaptions = useCallback(() => {
    subtitleSelectionWasManualRef.current = true;
    setPendingStoredSubtitle(null);
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

  // A choice in the subtitle menu wins over a reused track still waiting for
  // the inventory.
  const handleMenuSubtitleSelect = useCallback(
    (index: number | null, inventoryTrack?: SubtitleInventoryItemV3) => {
      setPendingStoredSubtitle(null);
      handleSubtitleSelect(index, inventoryTrack);
    },
    [handleSubtitleSelect],
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

  // Select a reused stored track once the inventory lists it, refreshing the
  // inventory once if it predates the track.
  useEffect(() => {
    if (!pendingStoredSubtitle) return;
    // A newer job took over while this track waited for the inventory.
    if (acceptedSubtitleJobRef.current !== pendingStoredSubtitle.jobId) {
      setPendingStoredSubtitle(null);
      return;
    }
    const key = `stored-${pendingStoredSubtitle.subtitleId}`;
    const track = subtitleUrls.find((t) => syncKeyOf(t) === key);
    if (track) {
      setPendingStoredSubtitle(null);
      resumeFromTranslationPause();
      setLiveTranslation(null);
      setLiveCues([]);
      handleSubtitleSelect(track.index);
    } else if (!pendingStoredSubtitle.refreshed) {
      setPendingStoredSubtitle({ ...pendingStoredSubtitle, refreshed: true });
      onRefreshSubtitles?.(getSubtitleStartPosition());
    }
  }, [
    pendingStoredSubtitle,
    subtitleUrls,
    handleSubtitleSelect,
    onRefreshSubtitles,
    getSubtitleStartPosition,
    resumeFromTranslationPause,
  ]);

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
        case "subtitle_timing_changed": {
          // A subtitle of this file was retimed. Its URL already serves the
          // new timing; the sync hook reloads the track if it is on screen
          // and refreshes the status the subtitle menu shows.
          if (event.payload.file_id === activeFileId) {
            subtitleTimingChanged(event.payload.sync_key);
          }
          break;
        }
        case "subtitle_sync_updated": {
          // A sync job of this file's subtitle moved on: queued, a step of
          // its progress, or how it ended.
          if (event.payload.file_id === activeFileId) {
            subtitleSyncUpdated(event.payload);
          }
          break;
        }
        case "subtitle_translation_started": {
          const payload = event.payload;
          if (!isForActiveStream(payload) || matchesLiveTranslation(payload)) break;
          acceptedSubtitleJobRef.current = String(payload.job_id);
          setPendingStoredSubtitle(null);
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
      subtitleSyncUpdated,
      subtitleTimingChanged,
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

  // Read by the transport effect, which reruns on planRevision rather than on
  // every input the original-file route depends on.
  const originalRouteInputRef = useRef({ plan, browserEngine, nativeOriginalSupport, audioTracks });
  originalRouteInputRef.current = { plan, browserEngine, nativeOriginalSupport, audioTracks };

  // -- hls.js lifecycle --
  useEffect(() => {
    // "Played" belongs to one transport. A replacement that has not shown a
    // frame yet may be a route this browser cannot reach, so its network
    // failures go through startup recovery rather than the reconnect.
    streamPlayedRef.current = false;
    transportIdleSinceRef.current = null;
    const video = videoRef.current;
    if (!video || !isPlayerReady || startupGuardRef.current?.hasFailed()) return;
    transportStartupPendingRef.current = true;
    // A source is loading, a replan's as well as the first: speed tests wait
    // for it as for a start.
    const sourceLoaded = sourceLoadStarted();

    let hls: HlsType | null = null;
    let engineSession: BrowserEngineSession | null = null;
    let destroyed = false;
    // Set when startup recovery loads a failed manifest again, so the reload's
    // own failure is not swallowed by the recovery cooldown.
    let manifestReloadPending = false;
    let playbackStarted = false;
    let autoplayInFlight = false;
    let autoplayAttempts = 0;
    let autoplayRetryTimer: ReturnType<typeof setTimeout> | null = null;
    let nativeHLSMetadataHandler: (() => void) | null = null;
    let directPlayMetadataHandler: (() => void) | null = null;
    // The plan's meter. A transport rebuilt for the same plan measures into
    // the same one.
    const meterMedia = (segmented: boolean) => {
      const meter =
        mediaMeterRef.current?.planRevision === planRevision
          ? mediaMeterRef.current.meter
          : new MediaDataMeter(performance.now());
      mediaMeterRef.current = { planRevision, meter, segmented };
      return meter;
    };

    mediaRecoveryAttemptsRef.current = 0;
    setError(null);
    setAwaitingFirstFrame(true);

    const clearAutoplayRetry = () => {
      if (autoplayRetryTimer === null) return;
      clearTimeout(autoplayRetryTimer);
      autoplayRetryTimer = null;
    };

    const cleanupStartupListeners = () => {
      transportStartupPendingRef.current = false;
      clearAutoplayRetry();
      video.removeEventListener("loadeddata", attemptAutoplayWhenReady);
      video.removeEventListener("canplay", attemptAutoplayWhenReady);
      video.removeEventListener("loadedmetadata", attemptAutoplayWhenReady);
      if (nativeHLSMetadataHandler) {
        video.removeEventListener("loadedmetadata", nativeHLSMetadataHandler);
        nativeHLSMetadataHandler = null;
      }
      if (directPlayMetadataHandler) {
        video.removeEventListener("loadedmetadata", directPlayMetadataHandler);
        directPlayMetadataHandler = null;
      }
      directPlayOriginPendingRef.current = false;
    };

    // A source settled paused holds speed tests off only while it still
    // loads: hls.js until its buffer is full or reaches the end, the browser
    // decoder until it can play, since it reads only what it decodes, and the
    // element until it suspends its fetch.
    let pausedSourceLoading = false;
    const endHoldOnceLoaded = () => {
      if (destroyed || !pausedSourceLoading) return;
      const loaded = hls
        ? hls.bufferedToEnd || (hls.mainForwardBufferInfo?.len ?? 0) >= hls.maxBufferLength
        : engineSession
          ? video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA
          : video.networkState === HTMLMediaElement.NETWORK_IDLE;
      if (!loaded) return;
      pausedSourceLoading = false;
      sourceLoaded();
    };
    video.addEventListener("suspend", endHoldOnceLoaded);
    video.addEventListener("canplay", endHoldOnceLoaded);

    // Settles the player into a deliberate paused state: the startup guard is
    // told playback is viable so it does not report a bogus startup timeout,
    // and the first frame is shown with the controls up.
    const settlePaused = () => {
      playbackStarted = true;
      cleanupStartupListeners();
      startupGuardRef.current?.markPlaybackStarted();
      transportIdleSinceRef.current = Date.now();
      setAwaitingFirstFrame(false);
      setPlaying(false);
      pausedSourceLoading = true;
      endHoldOnceLoaded();
    };

    const attemptAutoplayWhenReady = () => {
      if (destroyed || playbackStarted || autoplayInFlight) return;
      if (startupGuardRef.current?.hasFailed()) return;
      if (connectionReplacedRef.current) {
        settlePaused();
        return;
      }
      // HAVE_FUTURE_DATA means the browser has enough media to advance beyond
      // the current frame. Starting earlier can produce a visible first-frame
      // freeze where audio advances before video begins moving.
      if (video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA) return;
      if (roomPlaybackActiveRef.current) {
        // The room decides: its latest command starts this player from where
        // the room is now. Until one has it playing, the player waits paused;
        // starting on its own would put it ahead of everyone at the room's
        // start, or leave it behind from 0 after the room already started.
        if (!roomStartRef.current?.(video)) {
          settlePaused();
          return;
        }
      } else if (!shouldAutoPlay) {
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
          if (roomPlaybackActiveRef.current && isAutoplayPolicyRejection(error)) {
            // The room's start needs a click in this browser, as when the
            // room's own play is refused.
            settlePaused();
            offerRoomJoinRef.current?.();
            return;
          }
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
          if (destroyed || startupGuardRef.current?.hasFailed()) return;

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

            // A fragment load that errors is neither aborted nor given an
            // end, so the meter is told it is done; hls.js retries it as a
            // new load.
            const failedLoads = new WeakSet<object>();
            hls.on(Hls.Events.ERROR, (_event, data) => {
              if (data.details === Hls.ErrorDetails.FRAG_LOAD_ERROR && data.stats)
                failedLoads.add(data.stats);
              // A load error (refused, reset, HTTP failure) is the transport
              // failing, and so is a timeout with no data received. hls.js
              // times the whole load, so a timeout on a request that was
              // still receiving is a slow link and is left out. Subtitle
              // playlists and segments are not media requests.
              const details = String(data.details);
              const loaded = (data as { stats?: { loaded?: number } }).stats?.loaded ?? 0;
              const subtitleLoad = details.startsWith("subtitle") || data.frag?.type === "subtitle";
              if (
                data.type === Hls.ErrorTypes.NETWORK_ERROR &&
                !subtitleLoad &&
                (details.endsWith("LoadError") || (details.endsWith("LoadTimeOut") && loaded <= 0))
              ) {
                mediaOutageRef.current = true;
                outageBufferedEndRef.current = bufferedEndSeconds(video);
              }
              if (!data.fatal || destroyed) return;

              console.error("[hls.js] Fatal error:", {
                type: data.type,
                details: data.details,
                reason: data.reason,
                url: data.frag?.url ?? data.url,
                error: data.error?.message,
              });

              // hls.js has already retried the request; a fatal network error
              // on a stream that played means the server went away.
              if (data.type === Hls.ErrorTypes.NETWORK_ERROR && reportConnectionLostRef.current()) {
                return;
              }

              const now = Date.now();
              // A reloaded manifest can fail again within the cooldown (a
              // parse error does at once); that failure must still reach the
              // startup guard, or the player waits for the startup timeout.
              const reloadFailed = manifestReloadPending;
              manifestReloadPending = false;
              if (!reloadFailed && now - lastRecoveryRef.current < 3000) return;
              lastRecoveryRef.current = now;

              if (data.type === Hls.ErrorTypes.NETWORK_ERROR) {
                if (startupGuardRef.current?.handleFatalNetworkError() ?? true) {
                  console.warn("[hls.js] Fatal network error, attempting recovery...");
                  // startLoad() never requests a manifest that failed to load,
                  // so it would leave the player waiting for the startup
                  // timeout. Load the source again instead.
                  if (
                    data.details === Hls.ErrorDetails.MANIFEST_LOAD_ERROR ||
                    data.details === Hls.ErrorDetails.MANIFEST_LOAD_TIMEOUT ||
                    data.details === Hls.ErrorDetails.MANIFEST_PARSING_ERROR
                  ) {
                    manifestReloadPending = !streamPlayedRef.current;
                    hls?.loadSource(effectiveStreamUrl);
                  } else {
                    hls?.startLoad();
                  }
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

            hls.on(Hls.Events.MEDIA_ENDED, (_event, data) => {
              if ((data as { stalled?: boolean }).stalled)
                video.dispatchEvent(new Event(MEDIA_ENDED_EVENT));
            });
            // Each load gets its own stats object, which hls.js fills in as
            // the fragment arrives. Subtitles are not the plan's media.
            const meter = meterMedia(true);
            hls.on(Hls.Events.FRAG_LOADING, (_event, { frag }) => {
              if (destroyed || frag.type === "subtitle") return;
              const stats = frag.stats;
              meter.watch(stats, () => ({
                bytes: stats.loaded,
                first: stats.loading.first,
                done: stats.aborted || stats.loading.end > 0 || failedLoads.has(stats),
              }));
            });
            hls.on(Hls.Events.FRAG_LOADED, (_event, { frag }) => {
              if (destroyed || frag.type === "subtitle") return;
              const { loaded, loading } = frag.stats;
              meter.received(frag.stats, loaded, loading.first, loading.end);
            });

            hls.on(Hls.Events.BUFFER_APPENDED, (_event, data) => {
              if (destroyed) return;
              // Media data arriving ends an outage. Not every browser fires
              // the element's `progress` for MediaSource appends.
              if (
                data?.parent !== "subtitle" &&
                mediaOutageRef.current &&
                bufferedEndSeconds(video) > outageBufferedEndRef.current
              ) {
                mediaOutageRef.current = false;
              }
              attemptAutoplayWhenReady();
            });
            hls.on(Hls.Events.FRAG_BUFFERED, endHoldOnceLoaded);
            hls.on(Hls.Events.BUFFER_EOS, endHoldOnceLoaded);

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
        const routeInput = originalRouteInputRef.current;
        const route =
          routeInput.browserEngine && routeInput.plan.delivery === "original_http"
            ? resolveOriginalPlaybackRoute({
                plan: routeInput.plan,
                streamUrl: effectiveStreamUrl,
                native: routeInput.nativeOriginalSupport ?? {
                  containers: [],
                  videoCodecs: [],
                  audioCodecs: [],
                },
                engine: routeInput.browserEngine,
                audioTracks: routeInput.audioTracks,
              })
            : ({ kind: "native" } as const);
        if (route.kind === "unsupported") {
          if (!reportCurrentPlanFailure(route.failure)) {
            setError("This browser cannot play this file.");
          }
          return;
        }
        if (route.kind === "engine") {
          // The original goes to the browser decode engine, which stands in
          // for the element's media pipeline; the readiness gate below sees
          // the same events either way. Its clock starts at the file's first
          // frame, so an origin measured by an earlier native load is dropped.
          setDirectPlayOriginSeconds(0);
          timelineOffsetRef.current = planTimelineOffsetRef.current;
          try {
            const { attachBrowserEngine } = await import("../engine");
            const surface = engineSurfaceRef.current;
            if (destroyed || !surface) return;
            startupGuardRef.current?.dispose();
            const meter = meterMedia(false);
            // The engine numbers its reads from 0 on each attach, and a
            // transport rebuilt for the plan measures into the same meter, so
            // this attach's reads get keys of their own.
            const reads: object[] = [];
            engineSession = attachBrowserEngine({
              video,
              surface,
              source: route.source,
              onMediaData: (request, bytes, first, last) => {
                if (!destroyed) meter.received((reads[request] ??= {}), bytes, first, last);
              },
              onFailure: (failure) => {
                if (destroyed) return;
                const fail = () => {
                  if (!reportCurrentPlanFailure(failure)) {
                    setError(`Playback error: ${failure.message ?? failure.classification}`);
                  }
                };
                // When Auto lowers on its own, a start that missed its deadline
                // lowers, as a long stall would.
                if (failure.classification === ENGINE_STARTUP_TIMEOUT) {
                  startDeadlinePassedRef.current(fail);
                  return;
                }
                // Like MEDIA_ERR_NETWORK on the element: a read that fails
                // after the stream played reconnects on the same route.
                if (
                  failure.classification === ENGINE_READ_FAILED &&
                  reportConnectionLostRef.current()
                ) {
                  return;
                }
                fail();
              },
            });
            setBrowserEngineActive(true);
            setEngineTransportRevision((revision) => revision + 1);
            attemptAutoplayWhenReady();
          } catch (error) {
            if (
              !destroyed &&
              !reportCurrentPlanFailure({
                classification: "player_initialization_error",
                message:
                  error instanceof Error ? error.message : "Failed to load the browser engine.",
              })
            ) {
              setError("Failed to load video player.");
            }
          }
          return;
        }
        // Direct play — set video src directly. Starting playback goes through
        // the same readiness gate as HLS rather than calling play() against a
        // src that has not loaded yet: a play issued at HAVE_NOTHING is racing
        // the load algorithm that is about to seek to the resume position, and
        // the spec has that algorithm reject it.
        video.src = effectiveStreamUrl;
        if (playsOriginalFile) {
          // The original file's clock can start at the container's start time
          // instead of zero. Load without a start position so currentTime at
          // loadedmetadata reports where the clock starts, fold that into the
          // timeline offset, then seek to the start position on the corrected
          // clock. Subtitles, seeks, and progress then share the media timeline
          // the server's streams use.
          directPlayOriginPendingRef.current = true;
          directPlayMetadataHandler = () => {
            directPlayMetadataHandler = null;
            directPlayOriginPendingRef.current = false;
            const origin = Number.isFinite(video.currentTime) ? Math.max(0, video.currentTime) : 0;
            const planOffset = planTimelineOffsetRef.current;
            timelineOffsetRef.current = planOffset - origin;
            setDirectPlayOriginSeconds(origin);
            // A seek made while the origin was unknown wins over the start position.
            const mediaTarget =
              pendingSeekTimeRef.current ?? toMediaTime(effectiveInitialPosition, planOffset);
            const target = toPlayerTime(mediaTarget, timelineOffsetRef.current);
            if (Math.abs(video.currentTime - target) > 0.01) video.currentTime = target;
            applyElementDuration(video);
            attemptAutoplayWhenReady();
          };
          video.addEventListener("loadedmetadata", directPlayMetadataHandler, { once: true });
        } else {
          video.currentTime = effectiveInitialPosition;
          attemptAutoplayWhenReady();
        }
      }
    }

    init();

    return () => {
      destroyed = true;
      cleanupStartupListeners();
      video.removeEventListener("suspend", endHoldOnceLoaded);
      video.removeEventListener("canplay", endHoldOnceLoaded);
      if (engineSession) {
        // Restores the element's own media pipeline before it is reset below.
        engineSession.destroy();
        engineSession = null;
        setBrowserEngineActive(false);
        setEngineTransportRevision((revision) => revision + 1);
      }
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
        // A removed player's media events never reach the document.
        mediaElementReleased(video);
      }
    };
    // `planRevision` is the single signal that the transport changed: two plans
    // can share a stream URL and still differ in protocol, timeline, or recipe.
  }, [
    applyElementDuration,
    effectiveStreamUrl,
    effectiveInitialPosition,
    isHlsStream,
    isPlayerReady,
    planRevision,
    playsOriginalFile,
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
      // Nothing can play until the reconnect hands over a new transport.
      if (connectionReplacedRef.current || connectionStatusRef.current !== "connected") {
        video.pause();
        return;
      }
      setPlaying(true);
      const idleSince = transportIdleSinceRef.current;
      transportIdleSinceRef.current = null;
      if (idleSince !== null && Date.now() - idleSince >= PROGRESSIVE_REMUX_IDLE_REOPEN_MS) {
        reopenIdleProgressiveRemuxRef.current();
      }
    };
    const onPause = () => {
      resetRoomCatchupRate();
      setPlaying(false);
      // Only a stream that played or has opened. The pause a teardown queues
      // arrives after the next transport has started over, before it has
      // metadata.
      if (streamPlayedRef.current || video.readyState >= HTMLMediaElement.HAVE_METADATA) {
        transportIdleSinceRef.current ??= Date.now();
      }
    };
    const clearBuffering = () => {
      if (bufferingTimerRef.current) {
        clearTimeout(bufferingTimerRef.current);
        bufferingTimerRef.current = null;
      }
      setBuffering(false);
    };
    const markPlaybackStarted = () => {
      startupGuardRef.current?.markPlaybackStarted();
      streamPlayedRef.current = true;
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
    // Until an original file's clock origin is known the element has no media;
    // its events are load resets. They must not settle a held seek or move the
    // shown position off where playback is heading.
    const resolveSeekAt = (actualTime: number) =>
      directPlayOriginPendingRef.current
        ? { currentTime: pendingSeekTime ?? subtitleFetchAnchorRef.current, pendingSeekTime }
        : resolvePendingSeekTime(actualTime, pendingSeekTime);
    // The media reached the viewer's held room request: the controls follow
    // the media again.
    const releaseHeldSeekAt = (actualTime: number) => {
      if (directPlayOriginPendingRef.current || roomHeldSeekRef.current === null) return;
      if (remainingRoomHeldSeek(roomHeldSeekRef.current, actualTime) === null) holdRoomSeek(null);
    };
    const onTimeUpdate = () => {
      const nextTime = toMediaTime(video.currentTime, timelineOffsetRef.current);
      releaseHeldSeekAt(nextTime);
      const resolved = resolveSeekAt(nextTime);
      setCurrentTime(resolved.currentTime);
      if (resolved.pendingSeekTime !== pendingSeekTime) {
        setPendingSeekTime(resolved.pendingSeekTime);
      }
      // A correction's reloaded media is playing. How long it took to load
      // becomes the lead for the next reload.
      if (
        !video.paused &&
        !video.seeking &&
        video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA &&
        roomReloadLanded(roomReloadBudgetRef.current, nextTime)
      ) {
        landRoomReload(roomReloadBudgetRef.current, performance.now());
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
      const seekedTime = toMediaTime(video.currentTime, timelineOffsetRef.current);
      releaseHeldSeekAt(seekedTime);
      const resolved = resolveSeekAt(seekedTime);
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
    const onDurationChange = () => applyElementDuration(video);
    const onProgress = () => {
      setBuffered(video.buffered);
      if (mediaOutageRef.current && bufferedEndSeconds(video) > outageBufferedEndRef.current) {
        mediaOutageRef.current = false;
      }
    };
    const onVolumeChange = () => {
      // A room seek pre-roll mutes the element for a moment. That mute is not
      // the viewer's, so it is neither shown nor saved; a viewer unmuting
      // meanwhile is kept for when the pre-roll ends.
      let viewerMuted = video.muted;
      const prerollMuted = watchTogetherSync.prerollMutedPreference();
      if (prerollMuted !== null) {
        if (!video.muted) {
          watchTogetherSync.setPrerollMutedPreference(false);
          video.muted = true;
        }
        viewerMuted = watchTogetherSync.prerollMutedPreference() ?? prerollMuted;
      }
      setVolume(video.volume);
      setMuted(viewerMuted);
      persistVolume(video.volume, viewerMuted);
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
      // Playback runs again, so media requests are not failing.
      mediaOutageRef.current = false;
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
      // The browser received no data for its idle time (the engine reports a
      // failed read the same way): the transport is failing, not slow.
      mediaOutageRef.current = true;
      outageBufferedEndRef.current = bufferedEndSeconds(video);
      if (watchTogetherRoomActive && watchTogetherSync.attachedSessionId === sessionId) {
        watchTogetherSync.reportBuffering();
      }
    };
    // Going offline fails the media requests; coming back online does not
    // bring their data yet. Either way the outage lasts until data arrives.
    const onNetworkChange = () => {
      mediaOutageRef.current = true;
      outageBufferedEndRef.current = bufferedEndSeconds(video);
    };
    const onError = () => {
      if (video.error) {
        if (video.error.code === MEDIA_ERR_NETWORK) {
          mediaOutageRef.current = true;
          outageBufferedEndRef.current = bufferedEndSeconds(video);
        }
        if (video.error.code === MEDIA_ERR_NETWORK && reportConnectionLost()) return;
        const message = video.error.message || "Unknown media element error";
        if (!reportCurrentPlanFailure({ classification: "decoder_error", message })) {
          setError(`Playback error: ${message}`);
        }
      }
    };
    const onVideoEnded = () => {
      // The stream finished, so there is nothing left to reopen.
      transportIdleSinceRef.current = null;
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
    window.addEventListener("offline", onNetworkChange);
    window.addEventListener("online", onNetworkChange);

    return () => {
      window.removeEventListener("offline", onNetworkChange);
      window.removeEventListener("online", onNetworkChange);
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
    applyElementDuration,
    holdRoomSeek,
    pendingSeekTime,
    reportConnectionLost,
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

  // Menus live inside the controls, so hiding the controls under an open menu
  // leaves it inert (and Safari keeps painting its backdrop-filter surface).
  const hasOpenPlayerMenu = useCallback(
    () => containerRef.current?.querySelector('[role="menu"]') != null,
    [],
  );

  const resetControlsTimer = useCallback(() => {
    setControlsVisible(true);
    clearControlsTimer();
    const scheduleHide = () => {
      hideTimerRef.current = setTimeout(() => {
        if (hasOpenPlayerMenu()) {
          scheduleHide();
          return;
        }
        if (videoRef.current && !videoRef.current.paused) {
          setControlsVisible(false);
        }
        hideTimerRef.current = null;
      }, 3000);
    };
    scheduleHide();
  }, [clearControlsTimer, hasOpenPlayerMenu]);

  const hideControlsOnMouseLeave = useCallback(() => {
    if (hasOpenPlayerMenu()) return;
    clearControlsTimer();
    setControlsVisible(false);
  }, [clearControlsTimer, hasOpenPlayerMenu]);

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

  // The report control is handed to whichever pill is showing, so both the skip and
  // the undo prompt offer it in the same place. It carries the installation serving
  // the route: without one there is nothing to call. The kind is taken per prompt,
  // so the tooltip names the segment actually on screen.
  const markerReportAction = (kind?: string) =>
    markerReportInstallationId && markerReportContentId ? (
      <MarkerReportButton
        installationId={markerReportInstallationId}
        contentId={markerReportContentId}
        kind={kind}
      />
    ) : null;

  useEffect(() => {
    if (!introPromptVisible || displayMode !== "foreground") return;

    const promptSelector = '[data-intro-skip-prompt="true"]';

    const handlePromptKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented) return;
      const key = pillKey(event, promptSelector, containerRef.current);
      if (!key) return;

      const handled = key === "select" ? selectIntroPrompt() : dismissIntroPrompt();
      if (!handled) return;
      event.preventDefault();
      event.stopImmediatePropagation();
    };

    document.addEventListener("keydown", handlePromptKeyDown, true);
    return () => document.removeEventListener("keydown", handlePromptKeyDown, true);
  }, [dismissIntroPrompt, displayMode, introPromptVisible, selectIntroPrompt]);

  // A pill takes focus only after keyboard input, and never from a scrub, an
  // open menu or a text field.
  const pillMayTakeFocus = () => {
    if (!lastInputWasKeyboardRef.current) return false;
    const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const interactionInProgress =
      active?.closest(
        'input, textarea, select, [contenteditable="true"], [role="dialog"], [role="menu"], [role="listbox"], [role="slider"]',
      ) !== null ||
      containerRef.current?.querySelector('[role="dialog"], [role="menu"], [role="listbox"]') !=
        null;
    return !interactionInProgress;
  };
  const focusIntroPromptOnMount = activeIntroPrompt !== null && pillMayTakeFocus();

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
    const video = videoRef.current;
    const onChange = () => {
      setIsFullscreen(isPlayerFullscreen(video));
      setNativeVideoFullscreen(isNativeVideoFullscreen(video));
    };

    // A player mounted for the next episode can start inside a fullscreen
    // host, so read the current state rather than waiting for a change.
    onChange();
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
  // Height the bar and its gap cover at the bottom of the player. PGS
  // bitmaps carry their own position, so they clear this whatever the text
  // position setting says.
  const subtitleHudInsetPx =
    displayMode === "foreground" && controlsVisible && controlBarHeight > 0
      ? controlBarHeight + SUBTITLE_HUD_GAP
      : 0;
  const subtitleLiftPx =
    subtitleSettings.position !== "top"
      ? Math.max(0, subtitleHudInsetPx - baseSubtitleBottomPx)
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
    activeSubtitleCueRevision,
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
    activeSubtitleCueRevision,
    engineTransportRevision,
  );

  // -- PGS rendering (client-side Blu-ray bitmaps) --
  // The server sends embedded PGS as a `.sup` sidecar because the web player
  // declares `embedded_bitmap`. A plan that still burns the track in (an older
  // selection, or a route that cannot carry the sidecar) is drawn by the
  // server, so the overlay stays off to avoid doubling it.
  const [pgsSubtitleState, setPGSSubtitleState] = useState("idle");
  const pgsCanvasRef = useRef<HTMLCanvasElement>(null);
  const { isActive: isPGSActive } = usePGSSubtitles({
    videoRef,
    canvasRef: pgsCanvasRef,
    subtitleUrls,
    activeSubtitleIndex,
    enabled: !isDetached && plan.subtitle.mode !== "burn_in",
    streamOriginSeconds: timelineOffsetSeconds,
    subtitleDelayMs,
    durationRef,
    fetchAnchorRef: subtitleFetchAnchorRef,
    videoFit,
    bottomInsetPx: subtitleHudInsetPx,
    onLoadState: setPGSSubtitleState,
  });
  const subtitleLoadState = isASSActive
    ? assSubtitleState
    : isPGSActive
      ? pgsSubtitleState
      : textSubtitleState;
  const subtitleSyncFeedback = useSubtitleSyncFeedback({
    sync: subtitleSync,
    tracks: subtitleUrls,
    activeKey: activeSyncKey,
    cueRevisions: syncCueRevisions,
    loadState: subtitleLoadState,
  });
  const subtitleSyncNotice = isDetached ? null : subtitleSyncFeedback.notice;

  // -- Reconnect subtitle handover --
  // The session a lost connection left behind. When a reconnect has to start
  // a new session, the first render of that session still holds the old
  // selection; the handover below replaces it with the granted one, and until
  // then nothing may send the stale track to the server.
  const reconnectFromSessionRef = useRef<string | null>(null);
  useEffect(() => {
    if (connectionStatus !== "connected") {
      reconnectFromSessionRef.current ??= sessionId;
      return;
    }
    // The session survived the reconnect: nothing to hand over.
    if (reconnectFromSessionRef.current === sessionId) reconnectFromSessionRef.current = null;
  }, [connectionStatus, sessionId]);
  const subtitleHandoverPending =
    reconnectFromSessionRef.current !== null && reconnectFromSessionRef.current !== sessionId;

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
  const lastPlanSubtitleRef = useRef({
    sessionId,
    planId: plan.plan_id,
    index: plan.selected_tracks.subtitle?.index ?? null,
  });
  useEffect(() => {
    const planSubtitleIndex = plan.selected_tracks.subtitle?.index ?? null;
    const previousPlan = lastPlanSubtitleRef.current;
    lastPlanSubtitleRef.current = { sessionId, planId: plan.plan_id, index: planSubtitleIndex };
    // A replan in the same session that moved the subtitle on its own (a
    // version change dropping it, or bringing the viewer's kept subtitle back)
    // is the server's selection to adopt, not a difference to send back. Only
    // when the player still showed the previous plan's track: a pick of the
    // viewer's own that has not landed yet still goes out below.
    if (
      previousPlan.sessionId === sessionId &&
      previousPlan.planId !== plan.plan_id &&
      previousPlan.index !== planSubtitleIndex &&
      activeSubtitleIndex === previousPlan.index
    ) {
      subtitleSelectionWasManualRef.current = true;
      requestedSubtitleTrackChangeRef.current = null;
      setActiveSubtitleIndex(planSubtitleIndex);
      return;
    }
    // Live AI cues belong to the client overlay, not the server inventory.
    // Leave the current plan alone until a real downloaded track is ready.
    if (activeSubtitleIndex === LIVE_SUBTITLE_INDEX) {
      requestedSubtitleTrackChangeRef.current = null;
      return;
    }
    // The selection is the lost session's until the handover applies the one
    // the reconnect's start was granted; asking for it now would re-request a
    // track the server may just have refused.
    if (subtitleHandoverPending) return;
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
    sessionId,
    subtitleHandoverPending,
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
  //
  // A new session that a reconnect started is the same viewing, not the next
  // title: it carries the viewer's subtitle state (including "off") in its
  // start request, so the player adopts what the server granted and pins it
  // rather than auto-selecting from the profile again. A track the server had
  // to drop (a refused burn-in) comes back as no subtitle.
  const grantedSubtitleIndexRef = useRef<number | null>(null);
  grantedSubtitleIndexRef.current = plan.selected_tracks.subtitle?.index ?? null;
  useEffect(() => {
    const reconnectFrom = reconnectFromSessionRef.current;
    if (reconnectFrom !== null && reconnectFrom !== sessionId) {
      reconnectFromSessionRef.current = null;
      subtitleSelectionWasManualRef.current = true;
      setActiveSubtitleIndex(grantedSubtitleIndexRef.current);
      return;
    }
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
    // The engine draws on a canvas beside the <video>, so the video's own
    // native fullscreen (the iPhone fallback) would show an empty player.
    toggleFullscreen(
      fullscreenRootRef?.current ?? containerRef.current,
      browserEngineActive ? null : videoRef.current,
    );
  }, [browserEngineActive, fullscreenRootRef]);

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
  const carryStreamRebuildRef = useRef(watchTogetherSync.carryStreamRebuild);
  useEffect(() => {
    carryStreamRebuildRef.current = watchTogetherSync.carryStreamRebuild;
  }, [watchTogetherSync.carryStreamRebuild]);

  // Seeks for room sync, remembered so a repeat of a seek still loading can
  // be recognized and skipped.
  const issueRoomSeek = useCallback(
    (seconds: number, commandId: string | null): boolean | Promise<boolean> => {
      roomIssuedSeekRef.current = {
        targetSeconds: seconds,
        issuedAtMs: performance.now(),
        commandId,
      };
      if (
        roomHeldSeekRef.current !== null &&
        remainingRoomHeldSeek(roomHeldSeekRef.current, seconds) === null
      ) {
        holdRoomSeek(null);
      }
      return performPlayerSeekRef.current(seconds);
    },
    [holdRoomSeek],
  );

  // A held request belongs to the playback session and room selection it was
  // made in; a new title or session will not answer it.
  const roomHeldSeekSelectionRevision = watchTogether.room?.selection_revision;
  useEffect(() => {
    abandonRoomHeldSeek();
  }, [abandonRoomHeldSeek, roomHeldSeekSelectionRevision, sessionId]);

  // A held request belongs to a live room connection; with none, nothing will
  // answer it.
  useEffect(() => {
    if (
      !watchTogetherRoomId ||
      watchTogether.closedReason ||
      watchTogether.connectionState !== "connected"
    ) {
      holdRoomSeek(null);
    }
  }, [
    holdRoomSeek,
    watchTogether.closedReason,
    watchTogether.connectionState,
    watchTogetherRoomId,
  ]);

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
      !sessionId ||
      (command.session_id && command.session_id !== sessionId) ||
      command.selection_revision !== roomSelectionRevision
    ) {
      lastRoomCommandIdRef.current = null;
      appliedRoomCommandIdRef.current = null;
      roomReferenceRef.current = null;
      roomStartRef.current = null;
      offerRoomJoinRef.current = null;
      return;
    }
    // A repeat run for the same command (a clock, plan, or callback update)
    // only refreshes roomStartRef below; the command is already scheduled or run.
    const repeat = command.command_id === lastRoomCommandIdRef.current;
    if (!repeat) {
      lastRoomCommandIdRef.current = command.command_id;
      appliedRoomCommandIdRef.current = null;
      // The room's seek for the held request. A seek for an earlier press in a
      // burst does not answer it.
      if (
        command.action === "seek" &&
        roomHeldSeekRef.current !== null &&
        remainingRoomHeldSeek(roomHeldSeekRef.current, command.position_seconds) === null
      ) {
        roomHeldSeekAnsweredRef.current = true;
      }

      if (roomCommandTimerRef.current !== null) {
        window.clearTimeout(roomCommandTimerRef.current);
        roomCommandTimerRef.current = null;
      }
    }

    const serverExecuteAt = Date.parse(command.execute_at);
    const localExecuteAt = Number.isFinite(serverExecuteAt)
      ? serverExecuteAt - watchTogether.serverTimeOffsetMs
      : Date.now();
    const delay = Math.max(0, localExecuteAt - Date.now());
    // The command's position is the room position at execute_at, advancing
    // from there while the room plays.
    const reference: RoomPositionReference = {
      positionSeconds: command.position_seconds,
      executeAtServerMs: localExecuteAt + watchTogether.serverTimeOffsetMs,
      playing: command.playback_state === "playing",
    };

    // Applies the command's position at execution; see decideRoomCommand.
    // Play state is the caller's: a play command plays at 1x afterwards unless
    // this starts a nudge.
    // Returns false when playback must not start: the element ended and the
    // room is at or past its end.
    const applyRoomPosition = (video: HTMLVideoElement): boolean => {
      // A play command is the room's new reference; it supersedes an earlier
      // seek target the element never settled on (one that landed more than
      // the settle tolerance away). Left pending, that target would pin the
      // shown time and hold the drift loop off for good. A seek still running
      // or a stream still being replaced is genuinely in progress and stays.
      const pendingSeekSeconds = pendingSeekTimeRef.current;
      if (
        command.action === "play" &&
        pendingSeekSeconds !== null &&
        !video.seeking &&
        !planChangeInFlightRef.current
      ) {
        releasePendingSeek(pendingSeekSeconds);
      }
      // Only a viewer that keeps playing through a play command keeps (or
      // starts) a nudge; everything else runs at 1x.
      if (command.action !== "play" || video.paused) resetRoomCatchupRate();
      if (command.action === "pause") video.pause();
      const reloadBudget = roomReloadBudgetRef.current;
      if (command.action === "seek") {
        // An explicit room seek supersedes any correction reload.
        abandonRoomReload(reloadBudget, performance.now());
        settleRoomReloads(reloadBudget);
      } else if (command.action === "pause" && reloadBudget.targetSeconds !== null) {
        // The room stopped; the reload no longer chases a moving position.
        abandonRoomReload(reloadBudget, performance.now());
      }

      // Projected to the actual execution moment: a late command lands where
      // the room is now, not where it was at execute_at.
      const targetPositionSeconds = roomPositionAt(
        reference,
        Date.now(),
        serverTimeOffsetRef.current,
      );
      const localPlaying = !video.paused;
      const targetLocallySeekable =
        canSeekAnywhere ||
        isNativePositionInRanges(
          video.seekable,
          toPlayerTime(targetPositionSeconds, timelineOffsetRef.current),
        );
      let decision = decideRoomCommand({
        action: command.action,
        targetPositionSeconds,
        localPositionSeconds:
          elementMediaPosition() ?? toMediaTime(video.currentTime, timelineOffsetRef.current),
        localPlaying,
        rateCapable: roomRateCapableRef.current,
        targetLocallySeekable,
      });
      if (command.action === "play" && video.ended) {
        // play() on an ended element starts over from the beginning. A room
        // at or past the movie's end leaves it ended; one still short of the
        // end is sought to first, however small the gap. The movie's runtime
        // comes from the server: a progressive window can end well before it.
        const localEndSeconds =
          mediaDurationSeconds(
            backendDurationRef.current,
            Number.isFinite(video.duration)
              ? toMediaTime(video.duration, timelineOffsetRef.current)
              : undefined,
          ) ?? Number.POSITIVE_INFINITY;
        if (targetPositionSeconds >= localEndSeconds - roomSyncDeadbandSeconds) return false;
        decision = { kind: "seek" };
      }

      const issued = roomIssuedSeekRef.current;
      if (
        decision.kind === "seek" &&
        roomSeekAlreadyPending(
          issued,
          pendingSeekTimeRef.current,
          targetPositionSeconds,
          performance.now(),
        )
      ) {
        // The previous command's seek to this position is still loading. A
        // second one would reload or rebuild the stream all over again; the
        // running one serves this command, pre-roll included.
        carryStreamRebuildRef.current(issued?.commandId ?? null, command.command_id);
        return true;
      }
      const targetBuffered = isNativePositionInRanges(
        video.buffered,
        toPlayerTime(targetPositionSeconds, timelineOffsetRef.current),
      );
      // A player without rate nudges takes any target it reaches in place
      // directly; only a load beyond that goes through the reload budget.
      const reloadNeeded =
        !targetBuffered && (roomRateCapableRef.current || !targetLocallySeekable);
      if (decision.kind === "seek" && command.action === "play" && reloadNeeded) {
        // A correction to media that is not buffered yet loads it: a range
        // request or a stream rebuild. Keep playing behind the room while an
        // earlier reload is still loading or the backoff runs; the server
        // repeats corrections that still apply.
        const now = performance.now();
        if (!roomReloadAllowed(reloadBudget, now)) return true;
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
        void Promise.resolve(issueRoomSeek(reloadTarget, command.command_id)).then((accepted) => {
          if (reloadBudget.generation !== reloadGeneration) return;
          if (accepted) noteRoomReloadLoading(reloadBudget);
          else abandonRoomReload(reloadBudget, performance.now());
        });
      } else if (decision.kind === "seek") {
        // This seek replaces a reload still loading toward an older target.
        if (reloadBudget.targetSeconds !== null) {
          abandonRoomReload(reloadBudget, performance.now());
        }
        if (!roomRateCapableRef.current && command.action === "play" && localPlaying) {
          // A correcting seek for a player without rate nudges: aimed ahead
          // by what earlier seeks fell short, and measured by the drift loop.
          const corrector = roomSeekCorrectorRef.current;
          noteRoomCorrectorSeek(corrector, performance.now());
          issueRoomSeek(
            roomCorrectorSeekTarget(corrector, targetPositionSeconds),
            command.command_id,
          );
        } else {
          issueRoomSeek(targetPositionSeconds, command.command_id);
        }
      } else if (decision.kind === "rate") {
        roomNudgeActiveRef.current = true;
        video.playbackRate = decision.rate;
      } else if (command.action === "play" && localPlaying) {
        // Already with the room; the drift loop ends any running nudge.
        settleRoomReloads(reloadBudget);
      }
      return true;
    };

    // Startup runs this once the media is playable (see roomStartRef). A play
    // that executed before then is decided here, against the position and
    // seekable ranges the element has now.
    roomStartRef.current = (video) =>
      reference.playing &&
      lastRoomCommandIdRef.current === command.command_id &&
      appliedRoomCommandIdRef.current === command.command_id &&
      applyRoomPosition(video);

    // The browser refused to start playback without a click: offer one that
    // joins the room where it is when clicked.
    const offerJoinPlayback = () => {
      resetRoomCatchupRate();
      showWatchTogetherNotice(
        "Your browser blocked automatic playback. Click to join playback.",
        "warning",
        () => {
          if (!isMountedRef.current || lastRoomCommandIdRef.current !== command.command_id) return;
          const currentVideo = videoRef.current;
          if (!currentVideo) return;
          roomCommandFinishedAtRef.current = performance.now();
          if (!applyRoomPosition(currentVideo)) return;
          void currentVideo
            .play()
            .then(() => {
              if (!isMountedRef.current || lastRoomCommandIdRef.current !== command.command_id)
                return;
              reportRoomReadyRef.current();
            })
            .catch(() => {
              if (!isMountedRef.current || lastRoomCommandIdRef.current !== command.command_id)
                return;
              resetRoomCatchupRate();
            });
        },
      );
    };
    offerRoomJoinRef.current = offerJoinPlayback;
    if (repeat) return;

    roomCommandTimerRef.current = window.setTimeout(() => {
      roomCommandTimerRef.current = null;
      void (async () => {
        const video = videoRef.current;
        if (!video) {
          return;
        }

        appliedRoomCommandIdRef.current = command.command_id;
        roomReferenceRef.current = reference;
        roomCommandFinishedAtRef.current = performance.now();
        // The room's seek for the held request, or a command that replaced it
        // before it ran, ends the hold. A newer request since resets it. The
        // seek itself keeps the pending target, which may be a stream rebuild
        // to the same place already loading; a replacing command drops it.
        if (roomHeldSeekAnsweredRef.current) {
          if (command.action === "seek") holdRoomSeek(null);
          else abandonRoomHeldSeek();
        }
        // A play that runs while this transport is still starting, before it
        // has playable media, waits for its startup (see roomStartRef): the
        // position and seekable ranges mean nothing yet, and a source still
        // loading lands wherever its startup puts it. Once a transport has
        // started, a play goes ahead as before and starts when data arrives.
        const awaitingMedia =
          command.action === "play" &&
          video.paused &&
          transportStartupPendingRef.current &&
          video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA;
        const playAllowed = !awaitingMedia && applyRoomPosition(video);

        if (command.action === "seek") {
          video.pause();
        }

        if (command.action === "play" && playAllowed) {
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
            offerJoinPlayback();
          }
        }

        if (command.playback_state === "waiting") {
          reportRoomReadyRef.current();
        }
      })()
        .catch(() => {})
        .finally(() => {
          if (lastRoomCommandIdRef.current === command.command_id) {
            roomCommandFinishedAtRef.current = performance.now();
          }
        });
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
    abandonRoomHeldSeek,
    canSeekAnywhere,
    elementMediaPosition,
    holdRoomSeek,
    issueRoomSeek,
    releasePendingSeek,
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

  // Continuous drift loop: while the room and this viewer both play, absorb
  // drift from the room reference with a rate nudge. It never seeks a player
  // that can nudge; drift too large for a nudge is the server's to correct,
  // and that correction arrives as a command. A player room sync does not
  // nudge seeks within reach instead. Around a command (scheduled,
  // just executed, or loading media) the loop only waits, before looking at
  // the room state: the snapshot that announces a command can arrive before
  // it executes, and a received command must not change the rate. Otherwise,
  // when the room or this element stops playing, a running nudge returns to 1x.
  const roomPhase = watchTogether.room?.phase;
  const roomPlaybackState = watchTogether.room?.playback_state;
  useEffect(() => {
    if (!roomPlaybackActive || !roomConnected) return;
    const tick = () => {
      const video = videoRef.current;
      if (!video) return;
      const reloadBudget = roomReloadBudgetRef.current;
      const monotonicMs = performance.now();
      if (reloadBudget.targetSeconds !== null && !roomReloadInFlight(reloadBudget, monotonicMs)) {
        // A reload that never landed; stop waiting for it.
        expireRoomReload(reloadBudget, monotonicMs);
      }
      if (
        roomCommandTimerRef.current !== null ||
        monotonicMs - roomCommandFinishedAtRef.current < roomSyncLoopSettleMs ||
        roomReloadInFlight(reloadBudget, monotonicMs)
      ) {
        return;
      }
      const corrector = roomSeekCorrectorRef.current;
      const reference = roomReferenceRef.current;
      const localPosition = elementMediaPosition();
      if (video.seeking || pendingSeekTimeRef.current !== null) {
        // Seek spacing counts from when a seek finished, not when it began.
        corrector.lastSeekAtMs = monotonicMs;
      }
      if (
        roomPhase !== "playing" ||
        roomPlaybackState !== "playing" ||
        !reference?.playing ||
        video.paused ||
        video.ended ||
        video.seeking ||
        video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA ||
        directPlayOriginPendingRef.current ||
        pendingSeekTimeRef.current !== null ||
        localPosition === null
      ) {
        if (roomNudgeActiveRef.current) resetRoomCatchupRate();
        return;
      }
      // The room position maps through the server clock, so it uses wall time.
      const roomPosition = roomPositionAt(reference, Date.now(), serverTimeOffsetRef.current);
      const drift = roomPosition - localPosition;
      if (!roomRateCapableRef.current) {
        if (roomNudgeActiveRef.current) resetRoomCatchupRate();
        // Playback runs again after a correcting seek: measure where it landed.
        observeRoomCorrector(corrector, drift);
        const target = roomCorrectorSeekTarget(corrector, roomPosition);
        const seekable =
          canSeekAnywhere ||
          isNativePositionInRanges(video.seekable, toPlayerTime(target, timelineOffsetRef.current));
        if (roomCorrectorSeekDue(corrector, drift, seekable, monotonicMs)) {
          noteRoomCorrectorSeek(corrector, monotonicMs);
          issueRoomSeek(target, appliedRoomCommandIdRef.current);
        }
        return;
      }
      const rate = nextRoomNudgeRate(drift, roomNudgeActiveRef.current);
      if (rate === 1) {
        if (roomNudgeActiveRef.current) resetRoomCatchupRate();
        // This viewer reached the room; the next drift starts a fresh backoff.
        if (Math.abs(drift) <= roomSyncDeadbandSeconds) settleRoomReloads(reloadBudget);
        return;
      }
      roomNudgeActiveRef.current = true;
      // Setting the rate never resumes a paused element.
      if (video.playbackRate !== rate) video.playbackRate = rate;
    };
    const intervalId = window.setInterval(tick, roomSyncLoopIntervalMs);
    return () => window.clearInterval(intervalId);
  }, [
    canSeekAnywhere,
    elementMediaPosition,
    issueRoomSeek,
    resetRoomCatchupRate,
    roomConnected,
    roomPhase,
    roomPlaybackActive,
    roomPlaybackState,
  ]);

  const handleMutedChange = useCallback(
    (m: boolean) => {
      const video = videoRef.current;
      if (!video) return;
      // During a room seek pre-roll the element stays muted until it ends.
      if (watchTogetherSync.setPrerollMutedPreference(m)) {
        setMuted(m);
        persistVolume(video.volume, m);
        return;
      }
      video.muted = m;
    },
    [watchTogetherSync],
  );

  const handleVolumeChange = useCallback(
    (v: number) => {
      const video = videoRef.current;
      if (!video) return;
      video.volume = v;
      if (v > 0 && video.muted) handleMutedChange(false);
    },
    [handleMutedChange],
  );

  const handleToggleMuted = useCallback(() => {
    handleMutedChange(!muted);
  }, [handleMutedChange, muted]);

  // -- Keyboard shortcuts --
  useKeyboardShortcuts(
    videoRef,
    handleFullscreenToggle,
    handlePlayPause,
    skipActions,
    toggleCaptions,
    handleToggleMuted,
    // The engine draws on a canvas, which cannot float; the control is hidden
    // too, so the shortcut does nothing rather than reject.
    browserEngineActive ? undefined : handleTogglePiP,
    displayMode === "foreground",
  );

  // The id is the plan's own quality label, handed back to the server verbatim.
  const handleQualitySelect = useCallback(
    (id: string, onAdopt?: (planRevision?: number) => void) => {
      // A new quality replaces the stream; reload pacing from the old one
      // does not apply to it.
      roomReloadBudgetRef.current = createRoomReloadBudget();
      roomIssuedSeekRef.current = null;
      roomSeekCorrectorRef.current = createRoomSeekCorrector();
      if (onQualitySelect) onQualitySelect(id, replanPosition(), undefined, onAdopt);
      else onAdopt?.(undefined);
    },
    [onQualitySelect, replanPosition],
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

  // A new plan is a new transport; the previous one's failed requests say
  // nothing about it.
  useEffect(() => {
    mediaOutageRef.current = false;
  }, [planRevision]);

  // The rate an adoption's media arrived at, where its transport can tell.
  // None after the browser changed networks while it played: the server files
  // a sample under the connection of the request that delivers it.
  const planThroughputKbps = (adoptionId: string | undefined) => {
    const current = mediaMeterRef.current;
    if (!current || String(current.planRevision) !== adoptionId) return undefined;
    if (String(networkChangedInRevisionRef.current) === adoptionId) return undefined;
    return current.meter.throughputKbps(performance.now());
  };

  // When playback keeps buffering, lower quality, ask first, or only report,
  // as the viewer's setting says. Rooms have their own lower-quality offer.
  const bufferingPlan = useMemo<BufferingPlan>(
    () => ({
      plan_id: plan.plan_id,
      delivered_kbps: plan.effective_recipe.bitrate_kbps ?? plan.source.bitrate_kbps ?? 0,
      quality: activeQualityId,
      active_quality: plan.active_quality,
      available_qualities: plan.available_qualities,
      degradation_warnings: plan.degradation_warnings.map((w) => w.code),
    }),
    [plan, activeQualityId],
  );
  // Intro skip, the other segment skips and Next Up take priority; a
  // buffering pill waits behind them with its timer frozen. A minimized
  // player shows no pill either, so the pill waits for it to come back.
  const higherPriorityPillActive =
    activeIntroPrompt !== null || nextEpisode.showCountdown || activeSkipMarker !== undefined;
  const bufferingQuality = useAutoQualityDownshift({
    videoRef,
    planRevision,
    suspended: watchTogetherRoomActive || !!watchTogetherRoomId,
    sessionId: bufferingTitleId ?? sessionId,
    attemptId: playbackAttemptId,
    mode: bufferingMode,
    plan: bufferingPlan,
    // Pills also wait while nobody can see them: a minimized player, a hidden
    // tab, or iPhone's native fullscreen view.
    obscured: higherPriorityPillActive || playerUnseen,
    playIntent: () =>
      shouldAutoPlay && !connectionReplacedRef.current && !roomPlaybackActiveRef.current,
    transportTrouble: () =>
      connectionStatus !== "connected" ||
      (typeof navigator !== "undefined" && navigator.onLine === false) ||
      mediaOutageRef.current,
    mediaData: () => {
      const current = mediaMeterRef.current;
      return current?.planRevision === planRevision
        ? { at: current.meter.lastDataAt(performance.now()), segmented: current.segmented }
        : undefined;
    },
    throughputKbps: () => planThroughputKbps(String(planRevision)),
    onReplan: (request, onAdopt) => {
      // A lowering also says what this plan's media arrived at: the server
      // plans with the lower figure, so one lowering reaches a link far
      // below the stalled stream instead of stepping down title by title.
      const observed =
        request.bandwidth_estimate_source === "observed_rebuffer"
          ? planThroughputKbps(String(planRevision))
          : undefined;
      onQualitySelect?.(
        request.quality_preference,
        replanPosition(),
        observed !== undefined ? { ...request, observed_throughput_kbps: observed } : request,
        onAdopt,
      );
    },
    onNotice: (key) => {
      const next: PlaybackNoticeState = {
        message: BUFFERING_QUALITY_COPY[key],
        tone: "info",
        visibleMs: BUFFERING_NOTICE_VISIBLE_MS,
      };
      // It waits while another notice holds the slot.
      if (noticeRef.current) setQueuedBufferingNotice(next);
      else setNotice(next);
    },
    onSummary: (summaryPlan, summary, pageEnding) => {
      const kbps = planThroughputKbps(summaryPlan.adoption_id);
      onBufferingSummary?.(
        summaryPlan,
        kbps !== undefined ? { ...summary, throughput_kbps: kbps } : summary,
        pageEnding,
      );
    },
    onKeptEstimate,
  });
  startDeadlinePassedRef.current = bufferingQuality.startDeadlinePassed;
  const bufferingPill = bufferingQuality.pill;
  // An outage shows the buffering spinner too, and says so: the browser
  // decoder keeps sending timeupdate while it waits, which clears `buffering`.
  const stalled = buffering || bufferingQuality.outage;
  let stallLabel: string | null = null;
  if (watchTogetherSync.catchingUp) stallLabel = "Catching up to the party";
  else if (bufferingQuality.outage) stallLabel = "Reconnecting…";
  const { answer: answerBufferingPill } = bufferingQuality;
  // The viewer's latest quality pick while it is pending. A pick that a newer
  // one replaced settles too, but must not end the newer one's suspension.
  const pendingPickRef = useRef<number | null>(null);
  const pickGenerationRef = useRef(0);
  const sendBufferingPick = useCallback(
    (send: (onAdopt: (planRevision?: number) => void) => void) => {
      const generation = ++pickGenerationRef.current;
      pendingPickRef.current = generation;
      answerBufferingPill("pick_sent");
      send((revision) => {
        const latest = pendingPickRef.current === generation;
        if (latest) pendingPickRef.current = null;
        if (revision !== undefined) {
          answerBufferingPill("quality_changed");
          // A newer pick still waiting stays pending.
          if (pendingPickRef.current !== null) answerBufferingPill("pick_sent");
        } else if (latest) answerBufferingPill("pick_refused");
      });
    },
    [answerBufferingPill],
  );
  // A new cap on the session's replans is a quality change (see the prop).
  const capForBuffering = bandwidthCapKbps ?? null;
  const capForBufferingRef = useRef(capForBuffering);
  useEffect(() => {
    if (capForBufferingRef.current === capForBuffering) return;
    capForBufferingRef.current = capForBuffering;
    answerBufferingPill("quality_changed");
  }, [answerBufferingPill, capForBuffering]);
  // An answer removes the pill; focus that was on it moves to the transport.
  const answerBuffering = useCallback(
    (answer: BufferingAnswer) => {
      const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      const pillHadFocus = active?.closest('[data-buffering-prompt="true"]') != null;
      answerBufferingPill(answer);
      if (pillHadFocus) focusTransportAfterIntroPrompt();
    },
    [answerBufferingPill, focusTransportAfterIntroPrompt],
  );
  // Select and Back reach the pill from the player root, as for intro skip.
  const bufferingPillKind = bufferingPill?.kind;
  useEffect(() => {
    if (!bufferingPillKind || displayMode !== "foreground") return;
    const promptSelector = '[data-buffering-prompt="true"]';
    const keydown = (event: KeyboardEvent) => {
      if (event.defaultPrevented) return;
      // The reconnect overlay covers the pill and takes Back first.
      if (connectionStatusRef.current !== "connected") return;
      const key = pillKey(event, promptSelector, containerRef.current);
      if (!key) return;
      const active = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      // Keep quality answers through its own click.
      if (key === "select" && active?.dataset.bufferingKeep === "true") return;
      event.preventDefault();
      event.stopImmediatePropagation();
      answerBuffering(
        key === "back" ? "back" : bufferingPillKind === "undo" ? "select_restore" : "select_lower",
      );
    };
    document.addEventListener("keydown", keydown, true);
    return () => document.removeEventListener("keydown", keydown, true);
  }, [answerBuffering, bufferingPillKind, displayMode]);

  // Rebuild pacing belongs to one stream in one room.
  useEffect(() => {
    roomReloadBudgetRef.current = createRoomReloadBudget();
    roomIssuedSeekRef.current = null;
    roomSeekCorrectorRef.current = createRoomSeekCorrector();
  }, [sessionId, watchTogetherRoomId]);

  // Stall history is judged per quality: choosing another starts over, and an
  // offer computed for the previous quality no longer applies.
  useEffect(() => {
    roomStallTimesRef.current = [];
    lowerQualityOfferedRef.current = false;
    setNotice((current) => (current?.actionLabel === LOWER_QUALITY_ACTION_LABEL ? null : current));
  }, [activeQualityId, sessionId, watchTogetherRoomId]);

  const lowerQualityChoiceRef = useRef(() =>
    lowerQualityOption(qualityOptions, activeQualityId, activeQuality, deliveredBitrateKbps),
  );
  useEffect(() => {
    lowerQualityChoiceRef.current = () =>
      lowerQualityOption(qualityOptions, activeQualityId, activeQuality, deliveredBitrateKbps);
    // A replan can leave no lower rung; an offer that cannot act is withdrawn.
    if (!lowerQualityChoiceRef.current()) {
      setNotice((current) =>
        current?.actionLabel === LOWER_QUALITY_ACTION_LABEL ? null : current,
      );
    }
  }, [activeQuality, activeQualityId, deliveredBitrateKbps, qualityOptions]);

  // A viewer who keeps stalling in a room cannot keep up at this quality.
  // Offer one step down, once per quality; the room's shared source is kept.
  useEffect(() => {
    if (roomStallSignal === 0 || lowerQualityOfferedRef.current) return;
    const now = Date.now();
    const recent = roomStallTimesRef.current.filter((at) => now - at < ROOM_STALL_WINDOW_MS);
    roomStallTimesRef.current = recent;
    if (recent.length < ROOM_STALLS_BEFORE_LOWER_QUALITY) return;
    if (!lowerQualityOption(qualityOptions, activeQualityId, activeQuality, deliveredBitrateKbps)) {
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
    activeQuality,
    activeQualityId,
    deliveredBitrateKbps,
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
          handleVolumeChange(Math.min(1, Math.max(0, nextVolume)));
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
            replanPosition(),
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
    [handleExit, handleVolumeChange, onPlanInvalidated, performPlayerSeek, replanPosition],
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

      {/* Buffering spinner (mid-playback stalls and outages only) */}
      {!isDetached && stalled && !roomSyncWaiting && !awaitingFirstFrame && isPlayerReady && (
        <div
          role="status"
          aria-label={stallLabel ?? "Buffering"}
          className="pointer-events-none absolute inset-0 z-30 flex flex-col items-center justify-center gap-3"
        >
          <div className="h-10 w-10 animate-spin rounded-full border-2 border-white/20 border-t-white" />
          {stallLabel ? (
            <span className="rounded-full bg-black/60 px-3 py-1 text-xs font-medium text-white/85">
              {stallLabel}
            </span>
          ) : (
            <span className="sr-only">Buffering</span>
          )}
        </div>
      )}

      {!isDetached && (notice || bufferingPill) ? (
        <PlaybackNoticeStack>
          {notice ? (
            <PlaybackNoticeOverlay
              title={notice.title}
              message={notice.message}
              tone={notice.tone}
              actionLabel={notice.actionLabel}
              onAction={notice.onAction}
            />
          ) : null}
          {bufferingPill ? (
            <BufferingQualityPill
              key={`${bufferingPill.planRevision}:${bufferingPill.kind}`}
              kind={bufferingPill.kind}
              remainingMs={bufferingPill.remainingMs}
              deadlineMs={bufferingPill.deadlineMs}
              focusOnMount={pillMayTakeFocus()}
              onAnswer={answerBuffering}
            />
          ) : null}
        </PlaybackNoticeStack>
      ) : null}

      {/* Mid-stream connection loss: retrying, or given up */}
      {!isDetached && connectionStatus !== "connected" && connectionStatus !== "ended" && (
        <div
          role={connectionStatus === "reconnecting" ? "status" : "alert"}
          className="absolute inset-0 z-40 flex items-center justify-center bg-black/80 px-6"
        >
          <div className="flex max-w-md flex-col items-center gap-4 text-center">
            {connectionStatus === "reconnecting" && (
              <div className="h-8 w-8 animate-spin rounded-full border-2 border-white/20 border-t-white" />
            )}
            <div className="space-y-1">
              <p className="text-base font-semibold text-white">
                {connectionStatus === "reconnecting"
                  ? "Reconnecting…"
                  : (connectionErrorTitle ?? "Connection lost")}
              </p>
              <p className="text-sm text-white/60">
                {connectionStatus === "reconnecting"
                  ? "The connection to the server was lost. Playback will continue where you left off."
                  : (connectionError ?? "Silo couldn't reconnect to the server.")}
              </p>
            </div>
            <div className="flex gap-2">
              {connectionStatus === "lost" && onRetryConnection && (
                <button
                  onClick={onRetryConnection}
                  disabled={isLeaving}
                  type="button"
                  className="rounded bg-white px-4 py-2 text-sm font-medium text-black hover:bg-white/90"
                >
                  Try again
                </button>
              )}
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
        </div>
      )}

      {/* Error state */}
      {!isDetached && error && connectionStatus === "connected" && (
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
      {/* Browser decode engine picture. It sits under the <video>, which has no
          source of its own then and stays the transparent, clickable surface
          every hook binds to. */}
      <div
        ref={engineSurfaceRef}
        aria-hidden="true"
        className="pointer-events-none absolute inset-0"
        style={browserEngineActive ? undefined : { display: "none" }}
      />

      <video
        ref={videoRef}
        className={`${isDetached ? "h-full w-full" : "absolute inset-0 h-full w-full"} ${
          videoFit === "cover" ? "object-cover" : "object-contain"
        }`}
        onClick={displayMode === "postroll" ? undefined : handleSurfaceTap}
        playsInline
        style={!isPlayerReady ? { visibility: "hidden" } : undefined}
      />

      {/* PGS bitmap subtitles. The canvas spans the player rather than the
          picture so a disc's subtitles can sit in the letterbox, where they
          were authored. */}
      <canvas
        ref={pgsCanvasRef}
        aria-hidden="true"
        className="pointer-events-none absolute inset-0 z-[5] h-full w-full"
        style={isPGSActive ? undefined : { display: "none" }}
      />

      {subtitleSyncNotice && (
        <SubtitleSyncIndicator
          notice={subtitleSyncNotice}
          onDismiss={subtitleSyncFeedback.dismiss}
        />
      )}

      {/* The sync indicator already says the corrected track is loading. */}
      {!isDetached &&
        activeSubtitleIndex !== null &&
        subtitleSyncNotice?.tone !== "progress" &&
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
          tracks are drawn by the PGS canvas above or burned in by the server.
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
          provenance={activeIntro}
          showConfidence={showMarkerConfidence}
          showSource={showMarkerSource}
          trailing={markerReportAction(activeIntro?.kind)}
        />
      )}
      {!isDetached && !activeIntroPrompt && !nextEpisode.showCountdown && activeSkipMarker && (
        <IntroSkipButton
          onSkip={skipMarker}
          label={MARKER_SKIP_LABELS[activeSkipMarker.kind]}
          controlsVisible={controlsVisible}
          provenance={activeSkipMarker}
          showConfidence={showMarkerConfidence}
          showSource={showMarkerSource}
          trailing={markerReportAction(activeSkipMarker.kind)}
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
          currentTime={roomHeldSeek ?? currentTime}
          duration={duration}
          buffered={buffered}
          chapters={chapters}
          trickplay={trickplay}
          trickplayUpdatedAt={trickplayUpdatedAt}
          onTrickplayError={onTrickplayError}
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
          onSubtitleSelect={handleMenuSubtitleSelect}
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
          subtitleSync={subtitleSync}
          audioTracks={audioTracks}
          activeAudioIndex={activeAudioIndex}
          onAudioSelect={onAudioSelect}
          qualityOptions={qualityOptions}
          activeQualityId={activeQualityId}
          activeQuality={plan.active_quality}
          isTranscoding={replanning}
          qualityError={replanError}
          onQualitySelect={(id) => {
            // Picking the quality already playing changes nothing. Any pill
            // goes as the pick is made, but a pick the session refuses is no
            // quality change: it counts once its plan is adopted, which the
            // session tells before it renders.
            if (id === activeQualityId) return;
            sendBufferingPick((onAdopt) => handleQualitySelect(id, onAdopt));
          }}
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
              ? (fileId) => {
                  // A version the viewer chooses is a quality change, counted
                  // like a quality pick: pending until its plan is adopted.
                  sendBufferingPick((onAdopt) =>
                    onSwitchVersion(fileId, replanPosition(), onAdopt),
                  );
                }
              : undefined
          }
          playbackSpeed={playbackSpeed}
          onPlaybackSpeedChange={roomPlaybackActive ? undefined : setPlaybackSpeed}
          onTogglePiP={browserEngineActive ? undefined : handleTogglePiP}
          onPlayPause={handlePlayPause}
          onSeek={handlePlayerSeek}
          onVolumeChange={handleVolumeChange}
          onMutedChange={handleMutedChange}
          videoFit={videoFit}
          onVideoFitToggle={() =>
            setVideoFit((current) => (current === "cover" ? "contain" : "cover"))
          }
          onFullscreenToggle={
            browserEngineActive && !document.fullscreenEnabled ? undefined : handleFullscreenToggle
          }
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
          browserEngine={browserEngineActive}
          onClose={() => setShowPlaybackInfo(false)}
        />
      )}
    </div>
  );
}
