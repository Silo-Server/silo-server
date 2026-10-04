import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import { startPlaybackV2 } from "../start-v2";
import { hasSequencedProgress, stopSequencedSession } from "../session-mutations";
import {
  CONNECTION_LOST_ERROR,
  describePlanTerminal,
  describePlaybackTransportError,
} from "../playback-errors";
import { isTransientPlayerRequestError, PlayerFetchError } from "../player-fetch";
import { useCodecDetection } from "./useCodecDetection";
import {
  buildClientCapabilitiesV3,
  buildClientPlaybackContextV3,
  detectBandwidthEstimateKbpsV3,
  detectMeteredV3,
} from "../client-context-v3";
import { buildRouteEventV3 } from "../route-events-v3";
import { reportSessionRouteEventV2 } from "../route-events-v2";
import { replanV2 } from "../lifecycle-v2";
import { takePlaybackIntent } from "../first-frame";
import { buildPlayerStreamUrl } from "../stream-url";
import { randomUUID } from "@/lib/uuid";
import {
  FEATURE_OUTPUT_CHANGE_V3,
  MAX_ATTEMPT_COUNT_V3,
  MAX_ATTEMPTED_PLAN_KEYS_V3,
  QUALITY_ORIGINAL_V3,
  type DecisionResponseV3,
  type FailureV3,
  type PlanV3,
  type RouteEventNameV3,
  type SubtitleInventoryItemV3,
} from "../protocol-v3";
import {
  buildReplanRequestV3,
  buildStartRequestV3,
  routeEventPlanIdentityV3,
  VIDEO_CLIENT_FEATURES_V3,
  type ReplanOptions,
} from "../playback-session-wire-v3";
import type {
  PlayerFileVersion,
  PlayerPlaybackVariant,
  PlayerSubtitleInfo,
  ResumeHints,
} from "../types";

interface PlaybackSessionState {
  /**
   * The server's plan, verbatim. It is the single source of truth for the
   * stream URL, the timeline, the selected tracks, the subtitle inventory and
   * the quality menu — the client derives none of those itself any more.
   */
  plan: PlanV3 | null;
  /**
   * Bumped every time a new plan is adopted. Consumers key stream-reload
   * effects on it rather than on object identity.
   */
  planRevision: number;
  streamUrl: string | null;
  sessionId: string | null;
  playbackAttemptId: string | null;
  mediaFileId: number | null;
  initialPosition: number;
  audioTrackIndex: number;
  durationSeconds: number | null;
  subtitleUrls: PlayerSubtitleInfo[];
  qualityPreference: string;
  shouldAutoPlay: boolean;
  loading: boolean;
  replacing: boolean;
  replanning: boolean;
  errorTitle: string | null;
  errorReason?: string | null;
  error: string | null;
  initialSubtitleErrorTitle: string | null;
  initialSubtitleError: string | null;
  /**
   * Whether a stream that already played is connected to the server.
   * `reconnecting` while the player retries after a mid-stream drop, `lost`
   * once it gave up; `connectionError` then says why.
   */
  connectionStatus: PlaybackConnectionStatus;
  connectionErrorTitle: string | null;
  connectionError: string | null;
}

export type PlaybackConnectionStatus = "connected" | "reconnecting" | "lost";

// Mid-stream reconnect backoff: 1s, 2s, 4s, 8s, then every 15s, for about two
// and a quarter minutes in total before the player gives up and asks the
// viewer. A cycle that starts within RECONNECT_STABLE_MS of the previous one
// recovering continues that cycle's budget, so a server that answers the API
// but drops every stream cannot keep the player retrying forever.
export const RECONNECT_BASE_DELAY_MS = 1_000;
export const RECONNECT_MAX_DELAY_MS = 15_000;
export const RECONNECT_MAX_ATTEMPTS = 12;
const RECONNECT_STABLE_MS = 30_000;

/** Delay before reconnect attempt `attempt` (0-based). */
export function reconnectDelayMs(attempt: number): number {
  return Math.min(RECONNECT_MAX_DELAY_MS, RECONNECT_BASE_DELAY_MS * 2 ** Math.max(0, attempt));
}

/**
 * Whether a failed reconnect request is worth repeating later: the server was
 * unreachable or overloaded, or it asked for a retry. Anything else is its
 * considered answer.
 *
 * `installation_changed` differs by step. A replan always carries the old
 * session's installation, so repeating it cannot succeed; it falls through to
 * a fresh start instead. A refused start has already dropped the cached
 * capabilities, so the next start reads the new installation and can succeed.
 */
function isRetryableReconnectError(error: unknown, step: "replan" | "start"): boolean {
  if (isTransientPlayerRequestError(error)) return true;
  return (
    error instanceof PlayerFetchError &&
    (error.status === 408 ||
      error.status === 429 ||
      error.code === "replan_in_progress" ||
      (step === "start" && error.code === "installation_changed"))
  );
}

/**
 * The tracks a reconnect's fresh start asks for: the ones the lost plan was
 * playing. A burned-in subtitle is flagged so a refusal falls back to playing
 * without it, as the initial start does for bitmap tracks.
 */
function reconnectTrackSelection(plan: PlanV3): {
  audioIndex?: number;
  subtitleIndex?: number;
  subtitleBurnIn: boolean;
} {
  const subtitleIndex = plan.selected_tracks.subtitle?.index;
  const subtitleBurnIn =
    subtitleIndex !== undefined &&
    (plan.subtitle.mode === "burn_in" ||
      plan.subtitle.inventory.some(
        (item) => item.combined_index === subtitleIndex && item.delivery === "burn_in_only",
      ));
  return { audioIndex: plan.selected_tracks.audio?.index, subtitleIndex, subtitleBurnIn };
}

type LoadSessionOutcome =
  | { kind: "adopted" }
  | { kind: "refused"; failure: PlaybackSessionErrorState }
  | { kind: "failed"; error: unknown }
  | { kind: "superseded" };

interface PlaybackSessionErrorState {
  title: string;
  message: string;
}

export interface UsePlaybackSessionResult extends PlaybackSessionState {
  /** Starts a fresh session against another file (edition/version switch). */
  switchVersion: (fileId: number, currentPosition: number) => void;
  /** `track_change` replan selecting another audio track by combined index. */
  switchAudioTrack: (index: number, currentPosition: number) => void;
  /**
   * `track_change` replan selecting (or clearing) the server-side subtitle
   * track. Only needed for tracks the server has to render — burn-in and
   * conversion. Sidecar tracks are fetched from the plan's inventory and drawn
   * by the client without involving the server.
   */
  changeSubtitleTrack: (combinedIndex: number | null, currentPosition: number) => void;
  /** `quality_change` replan for a label taken from `plan.available_qualities`. */
  changeQuality: (label: string, currentPosition: number) => void;
  /** `failure_recovery` replan after the client could not play the plan. */
  recoverFromFailure: (failure: FailureV3, currentPosition: number) => void;
  /**
   * A stream that already played lost its connection to the server. Retries
   * with backoff until the server answers, then resumes at `positionSeconds`,
   * playing again only when `resume` is set.
   */
  recoverConnection: (positionSeconds: number, resume: boolean) => void;
  /** Starts a fresh reconnect cycle after the previous one gave up. */
  retryConnection: () => void;
  /**
   * `failure_recovery` replan for a plan the *server* invalidated over the
   * realtime `plan_invalidated` command. Resolves to whether a replacement plan
   * is now playing; the caller reports that back as the command's result.
   */
  invalidatePlan: (planId: string, reason: string, currentPosition: number) => Promise<boolean>;
  /**
   * `seek_reanchor` replan when the target lies outside the seekable window.
   * Resolves with whether a plan at the new position was adopted.
   */
  reanchorSeek: (positionSeconds: number) => Promise<boolean>;
  /** Re-reads the subtitle inventory by replanning with the selection unchanged. */
  refreshSubtitles: (currentPosition: number) => void;
  /** Folds a realtime-delivered inventory entry in without a server round trip. */
  applySubtitleTrack: (track: SubtitleInventoryItemV3) => void;
  /** Keeps transport state current for output-capability replans. */
  updatePlaybackState: (positionSeconds: number, playing: boolean) => void;
  /**
   * Called when a transport shows its first frame. Reports `first_frame` once
   * per playback attempt, with `first_frame_ms` measured from the viewer's
   * request when one timed it; later transports of the attempt are ignored.
   */
  reportFirstFrame: () => void;
  /** Reports a playback route event as a diagnostic. Never affects playback. */
  reportEvent: (
    event: RouteEventNameV3,
    extra?: {
      failureClassification?: string;
      fallbackReason?: string;
      diagnostics?: Record<string, string | number | boolean | undefined | null>;
    },
  ) => void;
}

function subtitleSourceOf(source: string): PlayerSubtitleInfo["source"] {
  switch (source) {
    case "external":
    case "embedded":
    case "downloaded":
      return source;
    default:
      return undefined;
  }
}

/**
 * Maps the plan's subtitle inventory onto the player's track shape.
 *
 * `index` is the server's combined ordinal, copied verbatim: it is the identity
 * the client echoes back on a track change, and the key every subtitle consumer
 * in the player looks tracks up by. Entries the server publishes as
 * `burn_in_only` have no URL and are kept anyway — they are selectable, and
 * selecting one is what asks the server to burn them in.
 */
function mapSubtitleInventory(
  inventory: SubtitleInventoryItemV3[],
  mediaFileId: number,
  config: PlayerConfig,
): PlayerSubtitleInfo[] {
  const token = config.getAccessToken();
  return inventory.map((item) => ({
    index: item.combined_index,
    media_file_id: mediaFileId,
    track_id: item.track_id,
    burn_in_only: item.delivery === "burn_in_only",
    language: item.language ?? "",
    codec: item.codec,
    label: item.label ?? item.language ?? `Track ${item.combined_index + 1}`,
    source: subtitleSourceOf(item.source),
    forced: item.forced,
    hearing_impaired: item.hearing_impaired,
    url: item.url ? buildPlayerStreamUrl(config.apiBaseUrl, item.url, token) : "",
    font_bundle_url: item.font_bundle_url
      ? buildPlayerStreamUrl(config.apiBaseUrl, item.font_bundle_url, token)
      : undefined,
  }));
}

/**
 * Projects a plan onto the session state consumers render from.
 *
 * `durationSeconds` comes from `plan.source.duration_seconds` and nowhere else:
 * the spec forbids substituting the playback engine's reported duration, which
 * on an HLS copy remux is only the length produced so far.
 */
function planToSessionState(
  plan: PlanV3,
  sessionId: string | null,
  playbackAttemptId: string,
  planRevision: number,
  qualityPreference: string,
  shouldAutoPlay: boolean,
  config: PlayerConfig,
): PlaybackSessionState {
  return {
    plan,
    planRevision,
    streamUrl: buildPlayerStreamUrl(config.apiBaseUrl, plan.stream.url, config.getAccessToken()),
    sessionId,
    playbackAttemptId,
    mediaFileId: plan.effective_media_file_id,
    initialPosition: plan.timeline.player_start_seconds,
    audioTrackIndex: plan.selected_tracks.audio?.index ?? 0,
    durationSeconds: plan.source.duration_seconds ?? null,
    subtitleUrls: mapSubtitleInventory(
      plan.subtitle.inventory,
      plan.effective_media_file_id,
      config,
    ),
    qualityPreference,
    shouldAutoPlay,
    loading: false,
    replacing: false,
    replanning: false,
    errorTitle: null,
    errorReason: null,
    error: null,
    initialSubtitleErrorTitle: null,
    initialSubtitleError: null,
    // A plan was just handed out, so the server is reachable.
    connectionStatus: "connected",
    connectionErrorTitle: null,
    connectionError: null,
  };
}

/**
 * Turns a v3 decision into an error state.
 *
 * A conforming decision has exactly two shapes: a plan or a terminal. The
 * fallback below is defensive handling for a malformed or future response; it
 * is not a third protocol outcome.
 */
function describeDecisionWithoutPlan(decision: DecisionResponseV3): PlaybackSessionErrorState {
  if (decision.terminal) {
    return describePlanTerminal(decision.terminal);
  }
  return {
    title: "Playback unavailable",
    message: "This server is not accepting playback requests right now.",
  };
}

function describePlaybackSessionError(
  error: unknown,
  fallbackMessage: string,
  phase: "start" | "update" = "start",
): PlaybackSessionErrorState {
  const transportError = describePlaybackTransportError(error, phase);
  if (transportError) {
    return transportError;
  }

  if (error instanceof Error && error.message.trim().length > 0) {
    return {
      title: "Playback unavailable",
      message: error.message,
    };
  }

  return {
    title: "Playback unavailable",
    message: fallbackMessage,
  };
}

/**
 * Owns the v3 playback session: the start decision, the plan it produced, and
 * every replan that supersedes it.
 *
 * 1. On mount: probe the browser → POST `/playback/start` with a v3 request →
 *    adopt the returned plan.
 * 2. Quality, track and recovery changes go to `/playback/{id}/replan`; each
 *    answers with a whole new plan, never a patch.
 * 3. On unmount: DELETE `/playback/{session_id}` with a keepalive fallback.
 */
export function usePlaybackSession(
  requestKey: string,
  versions: PlayerFileVersion[],
  playbackVariants: PlayerPlaybackVariant[] = [],
  fileId?: number,
  initialPosition = 0,
  forceInitialPosition = false,
  qualityPreference?: string | null,
  maxBitrateKbps?: number | null,
  resumeHints?: ResumeHints,
  explicitAudioTrackIndex?: number | null,
  initialSubtitleTrackIndexByFileId?: Record<number, number>,
  initialBitmapSubtitleTrackIndexByFileId?: Record<number, number>,
  allowAlternateVersions = true,
): UsePlaybackSessionResult {
  const config = usePlayerConfig();
  const probe = useCodecDetection();
  const capabilitiesSettled = probe.settled;
  const clientCapabilities = useMemo(() => buildClientCapabilitiesV3(probe), [probe]);
  const clientPlaybackContext = useMemo(() => buildClientPlaybackContextV3(probe), [probe]);
  const capabilityRequestKey = useMemo(
    () => JSON.stringify([clientCapabilities, clientPlaybackContext]),
    [clientCapabilities, clientPlaybackContext],
  );

  const [state, setState] = useState<PlaybackSessionState>({
    plan: null,
    planRevision: 0,
    streamUrl: null,
    sessionId: null,
    playbackAttemptId: null,
    mediaFileId: null,
    initialPosition: 0,
    audioTrackIndex: 0,
    durationSeconds: null,
    subtitleUrls: [],
    qualityPreference: qualityPreference?.trim() || "auto",
    shouldAutoPlay: true,
    loading: true,
    replacing: false,
    replanning: false,
    errorTitle: null,
    errorReason: null,
    error: null,
    initialSubtitleErrorTitle: null,
    initialSubtitleError: null,
    connectionStatus: "connected",
    connectionErrorTitle: null,
    connectionError: null,
  });

  const sessionIdRef = useRef<string | null>(null);
  const planRef = useRef<PlanV3 | null>(null);
  const planRevisionRef = useRef(0);
  const serverFeaturesRef = useRef<string[]>([]);
  const stateRef = useRef(state);
  const activeRequestKeyRef = useRef<string | null>(null);
  const activeCapabilityRequestKeyRef = useRef<string | null>(null);
  const playbackPositionRef = useRef(initialPosition);
  const startIntentRef = useRef({
    position: initialPosition,
    forceStartPosition: forceInitialPosition,
    // When the viewer asked for this request, for its first_frame_ms.
    intentAt: null as number | null,
  });
  const hasAdoptedPlanRef = useRef(false);
  const awaitingInitialPlayerPositionRef = useRef(false);
  const playbackPlayingRef = useRef(true);
  const playbackStartedRef = useRef(false);
  // Whether the viewer means playback to run, as of the current plan. Until
  // that plan's transport shows a frame, the reported transport state is the
  // teardown of the old one or a startup that never got going, so the intent
  // is the one the plan was adopted with.
  const planAutoPlayRef = useRef(true);
  const planTransportShownRef = useRef(false);
  const switchingRef = useRef(false);
  const loadSequenceRef = useRef(0);

  // v3 identity. `playback_attempt_id` spans one whole attempt chain (a start
  // and every replan that follows it); `plan_attempt_id` identifies the single
  // plan currently on screen. Both are minted by the client — the server mints
  // `plan_id` and `plan_attempt_key`, which the client only ever echoes.
  const playbackAttemptIdRef = useRef<string | null>(null);
  const planAttemptIdRef = useRef<string>(randomUUID());
  const attemptedPlanKeysRef = useRef<string[]>([]);
  const attemptCountRef = useRef(1);
  const replanInFlightRef = useRef(false);
  // The attempt whose first frame is still to be reported. `intentAt` is the
  // `performance.now()` of the viewer's request that started it, or null when
  // nothing timed it; the event is still sent, just without a duration.
  const firstFrameRef = useRef<{
    attemptId: string;
    intentAt: number | null;
    reported: boolean;
  } | null>(null);
  // Adoptions in flight, counted per load sequence: a start or a replan whose
  // decision has not been applied yet. The server commits a replacement plan —
  // and starts the copy-safety scan behind it — before the client can read the
  // response, so a `plan_invalidated` command can name a plan this client is
  // still adopting. Waiters registered here are woken once their own sequence
  // has nothing in flight, which lets an invalidation decide against the plan
  // that actually won.
  //
  // The key is what makes the wait bounded. A superseded request — a version
  // switch abandoned mid-flight, a start whose `fetch` never settles — is not a
  // candidate to own the session any more, so waiting for it decides nothing
  // and is worse than not waiting: the server's invalidation deadline is 8s,
  // and a session that misses it is stopped outright. Only the sequence that
  // currently owns the session can still change what the invalidation should
  // decide against, so only it is waited on.
  const adoptionsInFlightRef = useRef(new Map<number, number>());
  const adoptionWaitersRef = useRef<Array<{ loadSequence: number; resolve: () => void }>>([]);
  const pendingReplanRef = useRef<{
    options: ReplanOptions;
    loadSequence: number;
    retireSessionOnRefusal: boolean;
    resolve: (adopted: boolean) => void;
    planId: string;
  } | null>(null);
  const issueReplanRef = useRef<
    (
      options: ReplanOptions,
      retireSessionOnRefusal?: boolean,
      reconnect?: boolean,
    ) => Promise<boolean>
  >(async () => false);
  const qualityRef = useRef(qualityPreference?.trim() || "auto");
  // The running mid-stream reconnect, if any. `generation` invalidates the
  // timer and any request of a cycle that has ended, so nothing it started can
  // act after the viewer left, a new start took over, or a plan was adopted.
  const reconnectRef = useRef({
    active: false,
    generation: 0,
    attempts: 0,
    positionSeconds: 0,
    resume: true,
    timer: null as ReturnType<typeof setTimeout> | null,
    recoveredAt: Number.NEGATIVE_INFINITY,
  });
  const runReconnectAttemptRef = useRef<(generation: number) => Promise<void>>(async () => {});
  const beginReconnectRef = useRef<
    (positionSeconds: number, resume: boolean, freshBudget: boolean) => void
  >(() => {});

  useEffect(() => {
    stateRef.current = state;
  }, [state]);

  /**
   * Ends the running reconnect cycle, if any. `recovered` records when a cycle
   * got a plan back, so a stream that drops again right away continues that
   * cycle's backoff and budget rather than starting over.
   */
  const endReconnect = useCallback((recovered: boolean) => {
    const reconnect = reconnectRef.current;
    if (!reconnect.active) return;
    reconnect.active = false;
    reconnect.generation += 1;
    if (reconnect.timer !== null) {
      clearTimeout(reconnect.timer);
      reconnect.timer = null;
    }
    if (recovered) reconnect.recoveredAt = Date.now();
  }, []);

  const beginAdoption = useCallback((loadSequence: number) => {
    const inFlight = adoptionsInFlightRef.current;
    inFlight.set(loadSequence, (inFlight.get(loadSequence) ?? 0) + 1);
  }, []);

  /**
   * Counts one in-flight adoption out of its load sequence.
   *
   * A sequence's waiters are woken only when nothing is left in flight for it:
   * a queued replan is dispatched from its predecessor's `finally` before the
   * predecessor is counted out, so the count tracks the whole chain rather than
   * one request.
   */
  const endAdoption = useCallback((loadSequence: number) => {
    const inFlight = adoptionsInFlightRef.current;
    const remaining = (inFlight.get(loadSequence) ?? 0) - 1;
    if (remaining > 0) {
      inFlight.set(loadSequence, remaining);
      return;
    }
    inFlight.delete(loadSequence);
    const waiters = adoptionWaitersRef.current;
    if (waiters.length === 0) return;
    const settled = waiters.filter((waiter) => !inFlight.has(waiter.loadSequence));
    if (settled.length === 0) return;
    adoptionWaitersRef.current = waiters.filter((waiter) => inFlight.has(waiter.loadSequence));
    for (const waiter of settled) waiter.resolve();
  }, []);

  /**
   * Resolves once the sequence that currently owns the session has no start or
   * replan in flight, or null when it has none — callers act synchronously in
   * the common case rather than deferring a turn.
   *
   * A request from a superseded sequence is deliberately not waited for. It can
   * no longer install a plan (every path re-checks the sequence before adopting
   * one), so it has nothing left to say about what an invalidation should
   * decide against, and a hung one would otherwise hold the wait open past the
   * server's deadline and cost the live session its stream.
   */
  const awaitAdoptionSettled = useCallback((): Promise<void> | null => {
    const loadSequence = loadSequenceRef.current;
    if (!adoptionsInFlightRef.current.has(loadSequence)) return null;
    return new Promise<void>((resolve) => {
      adoptionWaitersRef.current.push({ loadSequence, resolve });
    });
  }, []);

  const reportEvent = useCallback(
    (
      event: RouteEventNameV3,
      extra?: {
        failureClassification?: string;
        fallbackReason?: string;
        diagnostics?: Record<string, string | number | boolean | undefined | null>;
      },
    ) => {
      const attemptId = playbackAttemptIdRef.current;
      if (!attemptId) return;
      const plan = planRef.current;
      const input = {
        event,
        playbackAttemptId: attemptId,
        ...routeEventPlanIdentityV3(plan, sessionIdRef.current, planAttemptIdRef.current),
        ...extra,
      };
      const sessionId = sessionIdRef.current;
      if (sessionId && hasSequencedProgress(sessionId)) {
        void reportSessionRouteEventV2(config, sessionId, buildRouteEventV3(input));
      }
      // A terminal start never produced a session; there is nothing to report it against.
    },
    [config],
  );

  /**
   * Adopts a decision as the live session, or surfaces its terminal.
   * Returns whether a plan was adopted.
   */
  const adoptDecision = useCallback(
    (
      decision: DecisionResponseV3,
      initialSubtitleFailure?: PlaybackSessionErrorState | null,
    ): boolean => {
      serverFeaturesRef.current = decision.server_features;
      const plan = decision.playback_plan;
      if (!plan) {
        const failure = describeDecisionWithoutPlan(decision);
        if (decision.terminal) {
          reportEvent("terminal", { failureClassification: decision.terminal.reason });
        }
        // The plan already on screen is left alone. A refused replan surfaces
        // its reason without taking away the stream that is still playing; a
        // refused start had no stream to take away in the first place.
        setState((current) => ({
          ...current,
          loading: false,
          replacing: false,
          replanning: false,
          errorTitle: failure.title,
          errorReason: decision.terminal?.reason ?? null,
          error: failure.message,
        }));
        return false;
      }

      // Any adopted plan proves the server is reachable and replaces the
      // transport, so it also ends a reconnect in progress. The new transport
      // plays only if the viewer was playing when the connection dropped; the
      // element has been paused since, so its reported state says nothing.
      if (reconnectRef.current.active) playbackPlayingRef.current = reconnectRef.current.resume;
      planAutoPlayRef.current = playbackPlayingRef.current;
      planTransportShownRef.current = false;
      endReconnect(true);
      const sessionId = plan.session_id ?? decision.session_id ?? sessionIdRef.current;
      planAttemptIdRef.current = randomUUID();
      planRef.current = plan;
      sessionIdRef.current = sessionId ?? null;
      planRevisionRef.current += 1;
      hasAdoptedPlanRef.current = true;
      if (
        Number.isFinite(plan.timeline.source_start_seconds) &&
        plan.timeline.source_start_seconds >= 0
      ) {
        // Replan positions use the source timeline. Seed it immediately so an
        // output refresh before the first timeupdate preserves server-resolved
        // resume state instead of falling back to the caller's default zero.
        playbackPositionRef.current = plan.timeline.source_start_seconds;
        awaitingInitialPlayerPositionRef.current = plan.timeline.source_start_seconds > 0;
      }

      setState((current) => ({
        ...planToSessionState(
          plan,
          sessionId ?? null,
          playbackAttemptIdRef.current ?? "",
          planRevisionRef.current,
          qualityRef.current,
          playbackPlayingRef.current,
          config,
        ),
        initialSubtitleErrorTitle:
          initialSubtitleFailure === undefined
            ? current.initialSubtitleErrorTitle
            : (initialSubtitleFailure?.title ?? null),
        initialSubtitleError:
          initialSubtitleFailure === undefined
            ? current.initialSubtitleError
            : (initialSubtitleFailure?.message ?? null),
      }));
      reportEvent("plan_selected");
      return true;
    },
    [config, endReconnect, reportEvent],
  );

  const requestStart = useCallback(
    async (
      targetFileId: number,
      position: number,
      forceStartPosition: boolean,
      playbackAttemptId: string,
      subtitleTrackIndex: number | undefined,
      audioTrackIndex: number | null | undefined = explicitAudioTrackIndex,
    ): Promise<DecisionResponseV3> => {
      const body = buildStartRequestV3({
        extraClientFeatures: VIDEO_CLIENT_FEATURES_V3,
        fileId: targetFileId,
        profileId: config.getProfileId() ?? "",
        playbackAttemptId,
        qualityPreference: qualityRef.current,
        allowAlternateVersions,
        position,
        forceStartPosition,
        explicitAudioTrackIndex: audioTrackIndex,
        subtitleTrackIndex,
        metered: detectMeteredV3(),
        bandwidthEstimateKbps: detectBandwidthEstimateKbpsV3(),
        bandwidthCapKbps: maxBitrateKbps,
        clientCapabilities,
        clientPlaybackContext,
      });

      return await startPlaybackV2(config, body);
    },
    [
      allowAlternateVersions,
      clientCapabilities,
      clientPlaybackContext,
      config,
      explicitAudioTrackIndex,
      maxBitrateKbps,
    ],
  );

  const stopSession = useCallback(
    async (sessionId: string) => {
      await stopSequencedSession(config, sessionId);
    },
    [config],
  );

  const retireActiveSession = useCallback(
    (expectedSessionId: string) => {
      if (sessionIdRef.current !== expectedSessionId) return;
      loadSequenceRef.current += 1;
      void stopSession(expectedSessionId).catch(() => {
        // Best effort — stale session will time out server-side.
      });
      planRef.current = null;
      sessionIdRef.current = null;
      planAttemptIdRef.current = randomUUID();
      setState((current) => {
        if (current.sessionId !== expectedSessionId) return current;
        return {
          ...current,
          plan: null,
          streamUrl: null,
          sessionId: null,
          mediaFileId: null,
          initialPosition: 0,
          audioTrackIndex: 0,
          durationSeconds: null,
          subtitleUrls: [],
          loading: false,
          replacing: false,
          replanning: false,
        };
      });
    },
    [stopSession],
  );

  /**
   * Picks which file to *request*. The server owns adaptation — this only
   * expresses which edition the viewer is resuming, which is knowledge the
   * server does not have.
   */
  const selectFileId = useCallback(
    (preferredFileId?: number) => {
      if (preferredFileId) return preferredFileId;
      if (resumeHints?.lastFileId) {
        const exact = versions.find((v) => v.file_id === resumeHints.lastFileId);
        if (exact) return exact.file_id;
      }
      const variantFileId = selectDefaultVariantFile(playbackVariants, versions, resumeHints);
      if (variantFileId) return variantFileId;
      return versions[0]?.file_id ?? null;
    },
    [playbackVariants, resumeHints, versions],
  );

  const loadSession = useCallback(
    async ({
      preferredFileId,
      position,
      forceStartPosition,
      allowPreserveExistingSessionOnError,
      replacementErrorMessage,
      initialErrorMessage,
      intentAt,
      reconnect = false,
      tracks,
    }: {
      preferredFileId?: number;
      position: number;
      forceStartPosition: boolean;
      allowPreserveExistingSessionOnError: boolean;
      replacementErrorMessage: string;
      initialErrorMessage: string;
      /** When the viewer asked for this start, for its first_frame_ms. */
      intentAt: number | null;
      /** The start is a reconnect attempt, which must not cancel itself. */
      reconnect?: boolean;
      /**
       * Track selection to start with instead of the initial request's, so a
       * restarted session keeps what the viewer picked during playback. An
       * absent `subtitleIndex` is an explicit "subtitles off": a start that
       * names no subtitle track plays without one. `subtitleBurnIn` marks a
       * track the server has to burn in, which gets the same retry without
       * subtitles as a bitmap track at the initial start when it is refused.
       */
      tracks?: { audioIndex?: number; subtitleIndex?: number; subtitleBurnIn?: boolean };
    }): Promise<LoadSessionOutcome> => {
      // Any other start supersedes a running reconnect: it owns the session now.
      if (!reconnect) endReconnect(false);
      const previousState = stateRef.current;
      const previousSessionId = sessionIdRef.current;
      const hasExistingSession = !!previousState.sessionId && !!previousState.streamUrl;
      const loadSequence = ++loadSequenceRef.current;
      const previousAttempt = {
        playbackAttemptId: playbackAttemptIdRef.current,
        planAttemptId: planAttemptIdRef.current,
        attemptedPlanKeys: [...attemptedPlanKeysRef.current],
        attemptCount: attemptCountRef.current,
      };
      const restorePreviousAttempt = () => {
        playbackAttemptIdRef.current = previousAttempt.playbackAttemptId;
        planAttemptIdRef.current = previousAttempt.planAttemptId;
        attemptedPlanKeysRef.current = previousAttempt.attemptedPlanKeys;
        attemptCountRef.current = previousAttempt.attemptCount;
      };

      setState((current) => ({
        ...current,
        loading: !hasExistingSession,
        replacing: hasExistingSession,
        errorTitle: hasExistingSession ? current.errorTitle : null,
        errorReason: null,
        error: hasExistingSession ? current.error : null,
        initialSubtitleErrorTitle: hasExistingSession ? current.initialSubtitleErrorTitle : null,
        initialSubtitleError: hasExistingSession ? current.initialSubtitleError : null,
        ...(reconnect
          ? {}
          : {
              connectionStatus: "connected" as const,
              connectionErrorTitle: null,
              connectionError: null,
            }),
      }));

      // A start begins a new attempt chain: fresh attempt id, empty loop guard.
      let playbackAttemptId = randomUUID();
      playbackAttemptIdRef.current = playbackAttemptId;
      attemptedPlanKeysRef.current = [];
      attemptCountRef.current = 1;

      const retirePreviousSession = (nextError?: { title: string; message: string }) => {
        if (previousSessionId) {
          void stopSession(previousSessionId).catch(() => {
            // Best effort — stale session will time out server-side.
          });
        }
        planRef.current = null;
        sessionIdRef.current = null;
        planAttemptIdRef.current = randomUUID();
        setState((current) => ({
          ...current,
          plan: null,
          streamUrl: null,
          sessionId: null,
          playbackAttemptId,
          mediaFileId: null,
          initialPosition: 0,
          audioTrackIndex: 0,
          durationSeconds: null,
          subtitleUrls: [],
          loading: false,
          replacing: false,
          replanning: false,
          errorTitle: nextError?.title ?? current.errorTitle,
          error: nextError?.message ?? current.error,
        }));
      };

      beginAdoption(loadSequence);
      try {
        const selectedFileId = selectFileId(preferredFileId);
        if (!selectedFileId) {
          throw new Error("No playable version found");
        }

        const startAudioTrackIndex = tracks ? tracks.audioIndex : explicitAudioTrackIndex;
        const decision = await requestStart(
          selectedFileId,
          position,
          forceStartPosition,
          playbackAttemptId,
          tracks ? tracks.subtitleIndex : initialSubtitleTrackIndexByFileId?.[selectedFileId],
          startAudioTrackIndex,
        );

        if (loadSequence !== loadSequenceRef.current) {
          const staleSessionId = decision.playback_plan?.session_id ?? decision.session_id;
          if (staleSessionId) {
            await stopSession(staleSessionId).catch(() => {
              // Best effort cleanup for stale session starts.
            });
          }
          return { kind: "superseded" };
        }

        let decisionToAdopt = decision;
        let initialSubtitleFailure: PlaybackSessionErrorState | null = null;
        const bitmapSubtitleTrackIndex = tracks
          ? tracks.subtitleBurnIn
            ? tracks.subtitleIndex
            : undefined
          : initialBitmapSubtitleTrackIndexByFileId?.[selectedFileId];
        if (!decision.playback_plan && bitmapSubtitleTrackIndex !== undefined) {
          initialSubtitleFailure = describeDecisionWithoutPlan(decision);
          if (decision.session_id) {
            void stopSession(decision.session_id).catch(() => {
              // Best effort cleanup for the refused subtitle-bearing start.
            });
          }

          // A fresh attempt id avoids colliding with start idempotency: the
          // retry intentionally changes the request by dropping the bitmap
          // subtitle that made the first plan impossible.
          const fallbackPlaybackAttemptId = randomUUID();
          playbackAttemptId = fallbackPlaybackAttemptId;
          playbackAttemptIdRef.current = fallbackPlaybackAttemptId;
          decisionToAdopt = await requestStart(
            selectedFileId,
            position,
            forceStartPosition,
            fallbackPlaybackAttemptId,
            undefined,
            startAudioTrackIndex,
          );
          if (!decisionToAdopt.playback_plan) {
            initialSubtitleFailure = null;
          }
        }

        if (loadSequence !== loadSequenceRef.current) {
          const staleSessionId =
            decisionToAdopt.playback_plan?.session_id ??
            decisionToAdopt.session_id ??
            decision.playback_plan?.session_id ??
            decision.session_id;
          if (staleSessionId) {
            await stopSession(staleSessionId).catch(() => {
              // Best effort cleanup for a superseded start/replan chain.
            });
          }
          return { kind: "superseded" };
        }

        const adopted = adoptDecision(decisionToAdopt, initialSubtitleFailure);
        if (adopted) {
          // A start opens a new attempt. Its first frame is still to come:
          // whatever the player shows until the new transport loads belongs to
          // the attempt it replaced.
          firstFrameRef.current = { attemptId: playbackAttemptId, intentAt, reported: false };
        }
        if (!adopted && hasExistingSession && allowPreserveExistingSessionOnError) {
          restorePreviousAttempt();
          setState((current) => ({
            ...current,
            loading: false,
            replacing: false,
            errorTitle: previousState.errorTitle,
            errorReason: previousState.errorReason,
            error: previousState.error,
          }));
          return { kind: "refused", failure: describeDecisionWithoutPlan(decisionToAdopt) };
        }
        if (!adopted) {
          retirePreviousSession();
          return { kind: "refused", failure: describeDecisionWithoutPlan(decisionToAdopt) };
        }
        if (adopted && previousSessionId && previousSessionId !== sessionIdRef.current) {
          void stopSession(previousSessionId).catch(() => {
            // Best effort — stale session will time out server-side.
          });
        }
        return { kind: "adopted" };
      } catch (err) {
        if (loadSequence !== loadSequenceRef.current) {
          return { kind: "superseded" };
        }

        if (hasExistingSession && allowPreserveExistingSessionOnError) {
          console.error(replacementErrorMessage, err);
          restorePreviousAttempt();
          setState((current) => ({
            ...current,
            loading: false,
            replacing: false,
          }));
          return { kind: "failed", error: err };
        }

        const nextError = describePlaybackSessionError(err, initialErrorMessage);
        retirePreviousSession(nextError);
        return { kind: "failed", error: err };
      } finally {
        endAdoption(loadSequence);
      }
    },
    [
      adoptDecision,
      beginAdoption,
      endAdoption,
      endReconnect,
      explicitAudioTrackIndex,
      initialBitmapSubtitleTrackIndexByFileId,
      initialSubtitleTrackIndexByFileId,
      requestStart,
      selectFileId,
      stopSession,
    ],
  );
  const loadSessionRef = useRef(loadSession);
  loadSessionRef.current = loadSession;

  useEffect(() => {
    if (!capabilitiesSettled) return;
    if (activeRequestKeyRef.current === requestKey) {
      return;
    }
    activeRequestKeyRef.current = requestKey;
    activeCapabilityRequestKeyRef.current = capabilityRequestKey;
    qualityRef.current = qualityPreference?.trim() || "auto";
    playbackPositionRef.current = initialPosition;
    const intentAt = takePlaybackIntent(requestKey);
    startIntentRef.current = {
      position: initialPosition,
      forceStartPosition: forceInitialPosition,
      intentAt,
    };
    hasAdoptedPlanRef.current = false;
    awaitingInitialPlayerPositionRef.current = false;
    playbackPlayingRef.current = true;
    playbackStartedRef.current = false;

    void loadSession({
      preferredFileId: fileId,
      position: initialPosition,
      forceStartPosition: forceInitialPosition,
      allowPreserveExistingSessionOnError: false,
      replacementErrorMessage: "Failed to replace playback request",
      initialErrorMessage: "Failed to start playback",
      intentAt,
    });
  }, [
    capabilityRequestKey,
    capabilitiesSettled,
    fileId,
    forceInitialPosition,
    initialPosition,
    loadSession,
    qualityPreference,
    requestKey,
  ]);

  // Clean up session on unmount.
  useEffect(() => {
    return () => {
      // A start can finish after unmount, before it has published a session ID.
      // Let that reply take the stale-start path and stop its own session
      // instead of adopting it into an abandoned player.
      loadSequenceRef.current += 1;
      // A viewer who leaves while the player reconnects stops the retries.
      endReconnect(false);
      const sid = sessionIdRef.current;
      if (!sid) return;
      // sendBeacon doesn't support DELETE, so the stop uses fetch keepalive.
      void stopSequencedSession(config, sid, true).catch(() => {
        // Best effort: the session expires server-side.
      });
    };
  }, [config, endReconnect]);

  /**
   * Issues one replan and adopts whatever plan comes back.
   *
   * Replans are serialized server-side behind a lease, so a second concurrent
   * request would earn a `409 replan_in_progress`; the in-flight guard here
   * means the client never asks for one.
   *
   * A `reconnect` replan is a reconnect attempt: a refusal is not adopted (the
   * caller falls back to a fresh start) and a failed request is rethrown for
   * the caller to classify instead of being surfaced as an error.
   */
  const replan = useCallback(
    async function issueReplan(
      options: ReplanOptions,
      retireSessionOnRefusal = false,
      reconnect = false,
    ): Promise<boolean> {
      const plan = planRef.current;
      const sessionId = sessionIdRef.current;
      const playbackAttemptId = playbackAttemptIdRef.current;
      if (!plan || !sessionId || !playbackAttemptId) return false;
      if (replanInFlightRef.current) {
        const isPendingFailureRecovery =
          options.operation === "failure_recovery" || options.operation === "seek_failure_recovery";
        const isPendingOutputChange = options.operation === "output_change";
        const queuedOperation = pendingReplanRef.current?.options.operation;
        const hasQueuedFailureRecovery =
          queuedOperation === "failure_recovery" || queuedOperation === "seek_failure_recovery";
        const hasQueuedOutputChange = queuedOperation === "output_change";
        if (
          options.operation === "seek_reanchor" &&
          hasQueuedOutputChange &&
          pendingReplanRef.current
        ) {
          // The output refresh will open a fresh route at its position anyway;
          // fold a later seek into that queued intent rather than silently
          // dropping the viewer's newest target.
          pendingReplanRef.current = {
            ...pendingReplanRef.current,
            options: {
              ...pendingReplanRef.current.options,
              positionSeconds: options.positionSeconds,
            },
          };
          return false;
        }
        const isPendingUserIntent =
          options.operation === "track_change" || options.operation === "quality_change";
        const hasQueuedUserIntent =
          queuedOperation === "track_change" || queuedOperation === "quality_change";
        const shouldQueue =
          isPendingFailureRecovery ||
          (isPendingUserIntent && !hasQueuedFailureRecovery) ||
          (isPendingOutputChange && !hasQueuedFailureRecovery && !hasQueuedUserIntent) ||
          (options.operation === "seek_reanchor" &&
            !hasQueuedFailureRecovery &&
            !hasQueuedOutputChange &&
            !hasQueuedUserIntent);
        if (shouldQueue) {
          pendingReplanRef.current?.resolve(false);
          return new Promise<boolean>((resolve) => {
            pendingReplanRef.current = {
              options,
              loadSequence: loadSequenceRef.current,
              retireSessionOnRefusal,
              resolve,
              planId: plan.plan_id,
            };
          });
        }
        return false;
      }

      const isFailureRecovery =
        options.operation === "failure_recovery" || options.operation === "seek_failure_recovery";
      if (isFailureRecovery && attemptCountRef.current > MAX_ATTEMPT_COUNT_V3) {
        setState((current) => ({
          ...current,
          replanning: false,
          errorTitle: "Playback failed",
          error: "Playback failed after repeated recovery attempts.",
        }));
        return false;
      }

      // On an intent change nothing failed, so the previous route stays
      // eligible: the loop guard and the recovery counter reset. Only a
      // recovery accumulates them.
      const attemptedPlanKeys = isFailureRecovery
        ? [...attemptedPlanKeysRef.current, plan.plan_attempt_key].slice(
            -MAX_ATTEMPTED_PLAN_KEYS_V3,
          )
        : [];
      const attemptCount = isFailureRecovery ? attemptCountRef.current : 1;

      const body = buildReplanRequestV3({
        ...options,
        extraClientFeatures: VIDEO_CLIENT_FEATURES_V3,
        plan,
        playbackAttemptId,
        replanRequestId: randomUUID(),
        planAttemptId: planAttemptIdRef.current,
        qualityPreference: qualityRef.current,
        attemptedPlanKeys,
        attemptCount,
        metered: detectMeteredV3(),
        bandwidthEstimateKbps: detectBandwidthEstimateKbpsV3(),
        bandwidthCapKbps: maxBitrateKbps,
        clientCapabilities,
        clientPlaybackContext,
      });

      const loadSequence = loadSequenceRef.current;
      // Sampled now, not when the request fails: a transport that failed is
      // torn down, and the pause that forces is not the viewer's.
      const playIntent = planTransportShownRef.current
        ? playbackPlayingRef.current
        : planAutoPlayRef.current;
      replanInFlightRef.current = true;
      beginAdoption(loadSequence);
      setState((current) => ({
        ...current,
        replanning: true,
        errorTitle: null,
        errorReason: null,
        error: null,
      }));

      try {
        const decision = await replanV2(config, sessionId, body);

        // A version switch or a fresh start that landed while this was in
        // flight owns the session now; this plan is already superseded.
        if (loadSequence !== loadSequenceRef.current) return false;

        if (isFailureRecovery) {
          attemptedPlanKeysRef.current = attemptedPlanKeys;
          attemptCountRef.current = Math.min(attemptCount + 1, MAX_ATTEMPT_COUNT_V3 + 1);
        } else {
          attemptedPlanKeysRef.current = [];
          attemptCountRef.current = 1;
        }

        if (reconnect && !decision.playback_plan) return false;

        // Keep a refused-start subtitle pinned off across unrelated replans.
        // A successful explicit subtitle choice is the one action that clears
        // the marker and lets the new server-selected track take ownership.
        const adopted = adoptDecision(
          decision,
          options.operation === "track_change" && options.subtitle !== undefined ? null : undefined,
        );
        if (!adopted && retireSessionOnRefusal) {
          const pending = pendingReplanRef.current;
          const pendingCanValidateOutput =
            pending?.loadSequence === loadSequence &&
            pending.options.operation !== "seek_reanchor" &&
            pending.options.operation !== "seek_failure_recovery";
          if (pending && pendingCanValidateOutput) {
            // The output refusal proves the predecessor route is incompatible.
            // Let a queued planner-backed intent try the latest output evidence,
            // but require it to retire the session too if it is refused. Frozen
            // seek replans cannot validate new output evidence, so they must not
            // postpone retirement.
            pendingReplanRef.current = {
              ...pending,
              retireSessionOnRefusal: true,
            };
          } else {
            retireActiveSession(sessionId);
          }
        }
        return adopted;
      } catch (err) {
        if (loadSequence !== loadSequenceRef.current) return false;
        if (reconnect) throw err;
        if (retireSessionOnRefusal) {
          console.error("Failed to refresh playback output", err);
          return false;
        }
        if (
          isFailureRecovery &&
          playbackStartedRef.current &&
          isRetryableReconnectError(err, "replan")
        ) {
          // The stream broke and the server could not be reached to replace
          // it: the connection failed, not necessarily the route. Reconnect
          // rather than strand the viewer on an error.
          beginReconnectRef.current(options.positionSeconds, playIntent, false);
          return false;
        }
        const nextError = describePlaybackSessionError(err, "Failed to update playback", "update");
        setState((current) => ({
          ...current,
          replanning: false,
          errorTitle: nextError.title,
          errorReason: null,
          error: nextError.message,
        }));
        return false;
      } finally {
        replanInFlightRef.current = false;
        setState((current) => (current.replanning ? { ...current, replanning: false } : current));

        const pendingReplan = pendingReplanRef.current;
        pendingReplanRef.current = null;
        if (pendingReplan?.loadSequence === loadSequenceRef.current) {
          const recoveryAppliesToCurrentPlan =
            pendingReplan.options.operation !== "failure_recovery" &&
            pendingReplan.options.operation !== "seek_failure_recovery"
              ? true
              : pendingReplan.planId === planRef.current?.plan_id;
          if (recoveryAppliesToCurrentPlan) {
            // A capability change can queue behind a replan created by an older
            // render. Dispatch through the latest callback so its request carries
            // the current output evidence rather than the closed-over snapshot.
            void issueReplanRef
              .current(pendingReplan.options, pendingReplan.retireSessionOnRefusal)
              .then(pendingReplan.resolve);
          } else {
            pendingReplan.resolve(false);
          }
        } else {
          pendingReplan?.resolve(false);
        }
        // Last: a queued replan dispatched just above has already counted
        // itself in, so waiters are not woken between the two links of a chain.
        endAdoption(loadSequence);
      }
    },
    [
      adoptDecision,
      beginAdoption,
      clientCapabilities,
      clientPlaybackContext,
      config,
      endAdoption,
      maxBitrateKbps,
      retireActiveSession,
    ],
  );
  issueReplanRef.current = replan;

  useEffect(() => {
    if (!capabilitiesSettled) return;
    if (
      activeRequestKeyRef.current !== requestKey ||
      activeCapabilityRequestKeyRef.current === capabilityRequestKey
    ) {
      return;
    }
    activeCapabilityRequestKeyRef.current = capabilityRequestKey;

    const plan = planRef.current;
    const sessionId = sessionIdRef.current;
    if (!plan || !sessionId) {
      const startIntent = startIntentRef.current;
      const resumeFromAdoptedPlan = hasAdoptedPlanRef.current;
      void loadSession({
        preferredFileId: fileId,
        position: resumeFromAdoptedPlan ? playbackPositionRef.current : startIntent.position,
        forceStartPosition: resumeFromAdoptedPlan ? true : startIntent.forceStartPosition,
        allowPreserveExistingSessionOnError: false,
        replacementErrorMessage: "Failed to refresh playback output",
        initialErrorMessage: "Failed to refresh playback output",
        // Before any plan was adopted, the viewer is still waiting on the
        // Play they pressed, so the replacement start keeps its clock. After
        // one, video has been shown and nobody pressed Play for this start.
        intentAt: resumeFromAdoptedPlan ? null : startIntent.intentAt,
      });
      return;
    }

    if (!serverFeaturesRef.current.includes(FEATURE_OUTPUT_CHANGE_V3)) {
      return;
    }

    void replan(
      {
        operation: "output_change",
        positionSeconds: playbackPositionRef.current,
      },
      true,
    );
  }, [
    capabilityRequestKey,
    capabilitiesSettled,
    fileId,
    loadSession,
    replan,
    requestKey,
    retireActiveSession,
  ]);

  const switchAudioTrack = useCallback(
    (index: number, currentPosition: number) => {
      const plan = planRef.current;
      if (!plan) return;
      if (plan.selected_tracks.audio?.index === index) return;
      void replan({
        operation: "track_change",
        positionSeconds: currentPosition,
        // Sending the index alone lets the server resolve the identity against
        // the effective file; sending a mismatched pair would be rejected.
        audio: { id: "", index },
      });
    },
    [replan],
  );

  const changeSubtitleTrack = useCallback(
    (combinedIndex: number | null, currentPosition: number) => {
      const plan = planRef.current;
      if (!plan) return;
      void replan({
        operation: "track_change",
        positionSeconds: currentPosition,
        subtitle: combinedIndex == null ? null : { id: "", index: combinedIndex },
      });
    },
    [replan],
  );

  const changeQuality = useCallback(
    (label: string, currentPosition: number) => {
      const normalized = label.trim() || QUALITY_ORIGINAL_V3;
      const previousPreference = qualityRef.current;
      qualityRef.current = normalized;
      setState((current) => ({ ...current, qualityPreference: normalized }));
      void replan({ operation: "quality_change", positionSeconds: currentPosition }).then(
        (adopted) => {
          if (adopted || qualityRef.current !== normalized) return;
          qualityRef.current = previousPreference;
          setState((current) =>
            current.qualityPreference === normalized
              ? { ...current, qualityPreference: previousPreference }
              : current,
          );
        },
      );
    },
    [replan],
  );

  const recoverFromFailure = useCallback(
    (failure: FailureV3, currentPosition: number) => {
      // While the server is unreachable a transport failure says nothing about
      // the route; the reconnect replaces the transport anyway.
      if (reconnectRef.current.active) return;
      reportEvent("plan_failed", {
        failureClassification: failure.classification,
        ...(failure.message ? { diagnostics: { message: failure.message } } : {}),
      });
      void replan({ operation: "failure_recovery", positionSeconds: currentPosition, failure });
    },
    [replan, reportEvent],
  );

  // -- Mid-stream reconnect --
  //
  // A stream that already played and then lost the server has not failed its
  // route, so this deliberately avoids `failure_recovery`, which would exclude
  // the route and could push a direct-play viewer onto a transcode. Each
  // attempt first asks for the current route again with a `track_change` that
  // changes nothing (the server keeps the route eligible and answers with a
  // fresh plan at the saved position). If the session did not survive — a
  // server restart answers 404 — it starts a new session at the same position
  // with the same tracks. Unreachable or overloaded answers back off; any
  // adopted plan ends the cycle; the viewer leaving or another start cancels
  // it.

  const scheduleReconnectAttempt = useCallback((delayMs: number) => {
    const reconnect = reconnectRef.current;
    if (reconnect.timer !== null) clearTimeout(reconnect.timer);
    const generation = reconnect.generation;
    reconnect.timer = setTimeout(() => {
      reconnect.timer = null;
      void runReconnectAttemptRef.current(generation);
    }, delayMs);
  }, []);

  const giveUpReconnect = useCallback(
    (failure: PlaybackSessionErrorState) => {
      endReconnect(false);
      setState((current) => ({
        ...current,
        replacing: false,
        replanning: false,
        connectionStatus: "lost",
        connectionErrorTitle: failure.title,
        connectionError: failure.message,
      }));
    },
    [endReconnect],
  );

  const beginReconnect = useCallback(
    (positionSeconds: number, resume: boolean, freshBudget: boolean) => {
      const reconnect = reconnectRef.current;
      if (reconnect.active) return;
      reconnect.active = true;
      reconnect.generation += 1;
      if (freshBudget || Date.now() - reconnect.recoveredAt >= RECONNECT_STABLE_MS) {
        reconnect.attempts = 0;
      }
      reconnect.positionSeconds = Math.max(0, positionSeconds);
      reconnect.resume = resume;
      if (reconnect.attempts >= RECONNECT_MAX_ATTEMPTS) {
        // The stream keeps dropping right after every recovery.
        giveUpReconnect(CONNECTION_LOST_ERROR);
        return;
      }
      setState((current) => ({
        ...current,
        connectionStatus: "reconnecting",
        connectionErrorTitle: null,
        connectionError: null,
      }));
      scheduleReconnectAttempt(freshBudget ? 0 : reconnectDelayMs(reconnect.attempts));
    },
    [giveUpReconnect, scheduleReconnectAttempt],
  );
  beginReconnectRef.current = beginReconnect;

  runReconnectAttemptRef.current = async (generation: number) => {
    const reconnect = reconnectRef.current;
    const isCurrent = () => reconnect.active && reconnect.generation === generation;
    if (!isCurrent()) return;
    // One request at a time: a start or replan already talking to the server
    // for this session decides first; check again once it has settled.
    if (replanInFlightRef.current || adoptionsInFlightRef.current.has(loadSequenceRef.current)) {
      scheduleReconnectAttempt(RECONNECT_BASE_DELAY_MS);
      return;
    }
    reconnect.attempts += 1;
    const retryLater = () => {
      if (reconnect.attempts >= RECONNECT_MAX_ATTEMPTS) {
        giveUpReconnect(CONNECTION_LOST_ERROR);
        return;
      }
      scheduleReconnectAttempt(reconnectDelayMs(reconnect.attempts));
    };
    const positionSeconds = reconnect.positionSeconds;

    if (planRef.current && sessionIdRef.current) {
      try {
        const adopted = await issueReplanRef.current(
          { operation: "track_change", positionSeconds },
          false,
          true,
        );
        // An adopted plan has already ended the cycle.
        if (adopted || !isCurrent()) return;
      } catch (error) {
        if (!isCurrent()) return;
        if (isRetryableReconnectError(error, "replan")) {
          retryLater();
          return;
        }
        // Any other answer means the session is gone (a restart, an expiry);
        // a fresh start at the same position is the authoritative check.
      }
    }

    const plan = planRef.current;
    const outcome = await loadSessionRef.current({
      preferredFileId: plan?.effective_media_file_id ?? stateRef.current.mediaFileId ?? undefined,
      position: positionSeconds,
      forceStartPosition: true,
      allowPreserveExistingSessionOnError: true,
      replacementErrorMessage: "Failed to restart playback after the connection was lost",
      initialErrorMessage: "Failed to restart playback",
      intentAt: null,
      reconnect: true,
      // The plan's selection is the viewer's: the player replans every
      // subtitle pick, sidecar or burned in, so the server's copy stays in
      // step with what is on screen. No subtitle there means subtitles off.
      tracks: plan ? reconnectTrackSelection(plan) : undefined,
    });
    if (!isCurrent()) return;
    if (outcome.kind === "failed") {
      if (isRetryableReconnectError(outcome.error, "start")) {
        retryLater();
        return;
      }
      giveUpReconnect(describePlaybackSessionError(outcome.error, CONNECTION_LOST_ERROR.message));
      return;
    }
    if (outcome.kind === "refused") {
      giveUpReconnect(outcome.failure);
    }
  };

  const recoverConnection = useCallback(
    (positionSeconds: number, resume: boolean) => {
      // Only a stream that exists can lose its connection.
      if (!planRef.current || !sessionIdRef.current) return;
      beginReconnect(positionSeconds, resume, false);
    },
    [beginReconnect],
  );

  const retryConnection = useCallback(() => {
    const reconnect = reconnectRef.current;
    beginReconnect(reconnect.positionSeconds, reconnect.resume, true);
  }, [beginReconnect]);

  // Coming back online is the best moment to try again.
  const reconnecting = state.connectionStatus === "reconnecting";
  useEffect(() => {
    if (!reconnecting) return;
    const onOnline = () => {
      const reconnect = reconnectRef.current;
      if (reconnect.active && reconnect.timer !== null) scheduleReconnectAttempt(0);
    };
    window.addEventListener("online", onOnline);
    return () => window.removeEventListener("online", onOnline);
  }, [reconnecting, scheduleReconnectAttempt]);

  /**
   * Replans off a plan the server invalidated mid-playback.
   *
   * This is an ordinary `failure_recovery`, deliberately: that operation is
   * what folds the current plan's attempt key into `attempted_plan_keys`, so
   * the route the server just disqualified is excluded from the replacement
   * plan without the client reasoning about deliveries at all. The plan
   * revision the adopted plan bumps rebuilds the transport and restores the
   * position, exactly as it does after a client-detected failure.
   *
   * A start or replan already in flight is waited out first. The server commits
   * a replacement plan and starts the copy-safety scan behind it *before* the
   * response reaches the client, so an invalidation can name a plan this client
   * has not adopted yet. Deciding against the plan currently on screen would
   * complete the command as a no-op and then let the pending response install
   * the very route the server just withdrew.
   */
  const invalidatePlan = useCallback(
    async (planId: string, reason: string, currentPosition: number): Promise<boolean> => {
      const settling = awaitAdoptionSettled();
      if (settling) await settling;
      const plan = planRef.current;
      if (!plan) return false;
      // The command names the plan the server invalidated. Once the client has
      // moved past it there is nothing to recover from, and replanning anyway
      // would evict a route the server never complained about. That stays true
      // for a plan id this client has never seen: it is a verdict for a route
      // that has already been replaced, not a reason to tear the session down.
      if (plan.plan_id !== planId) return true;
      const classification = reason.trim().slice(0, 64) || "plan_invalidated";
      reportEvent("plan_invalidated", { fallbackReason: classification });
      return replan({
        operation: "failure_recovery",
        positionSeconds: currentPosition,
        failure: { classification, message: "The server invalidated this plan." },
      });
    },
    [awaitAdoptionSettled, replan, reportEvent],
  );

  const reanchorSeek = useCallback(
    (positionSeconds: number): Promise<boolean> => {
      playbackPositionRef.current = positionSeconds;
      awaitingInitialPlayerPositionRef.current = false;
      reportEvent("seek_reanchor_requested");
      return replan({ operation: "seek_reanchor", positionSeconds });
    },
    [replan, reportEvent],
  );

  /**
   * Re-reads the subtitle inventory.
   *
   * There is no "refresh" operation in v3, and there does not need to be: a
   * `track_change` that changes nothing returns a fresh plan — inventory
   * included — without excluding the route already playing.
   */
  const refreshSubtitles = useCallback(
    (currentPosition: number) => {
      if (!planRef.current) return;
      void replan({ operation: "track_change", positionSeconds: currentPosition });
    },
    [replan],
  );

  /**
   * Folds a track the server pushed over the realtime socket into the plan's
   * inventory at the ordinal the server assigned it. The ordinal is never
   * derived client-side, which is why the payload carries the whole entry.
   */
  const applySubtitleTrack = useCallback(
    (track: SubtitleInventoryItemV3) => {
      const plan = planRef.current;
      if (!plan) return;
      const inventory = [
        ...plan.subtitle.inventory.filter((item) => item.combined_index !== track.combined_index),
        track,
      ].sort((a, b) => a.combined_index - b.combined_index);
      const nextPlan: PlanV3 = { ...plan, subtitle: { ...plan.subtitle, inventory } };
      planRef.current = nextPlan;
      setState((current) => ({
        ...current,
        plan: nextPlan,
        subtitleUrls: mapSubtitleInventory(inventory, nextPlan.effective_media_file_id, config),
      }));
    },
    [config],
  );

  const updatePlaybackState = useCallback((positionSeconds: number, playing: boolean) => {
    if (Number.isFinite(positionSeconds) && positionSeconds >= 0) {
      const isUninitializedPlayerZero =
        awaitingInitialPlayerPositionRef.current &&
        !playing &&
        positionSeconds === 0 &&
        playbackPositionRef.current > 0;
      if (!isUninitializedPlayerZero) {
        playbackPositionRef.current = positionSeconds;
        awaitingInitialPlayerPositionRef.current = false;
      }
    }
    if (playing) {
      playbackStartedRef.current = true;
      playbackPlayingRef.current = true;
    } else if (playbackStartedRef.current) {
      playbackPlayingRef.current = false;
    }
  }, []);

  const reportFirstFrame = useCallback(() => {
    // The current plan's transport is on screen: from here its reported state
    // is the viewer's intent.
    planTransportShownRef.current = true;
    const attempt = firstFrameRef.current;
    // Until a start's plan is adopted, the attempt id has already moved on and
    // the frame on screen belongs to the attempt being replaced.
    if (!attempt || attempt.reported || attempt.attemptId !== playbackAttemptIdRef.current) return;
    attempt.reported = true;
    reportEvent(
      "first_frame",
      attempt.intentAt === null
        ? undefined
        : { diagnostics: { first_frame_ms: Math.round(performance.now() - attempt.intentAt) } },
    );
  }, [reportEvent]);

  const switchVersion = useCallback(
    (newFileId: number, currentPosition: number) => {
      if (!allowAlternateVersions) return;
      if (switchingRef.current) return;
      if (newFileId === stateRef.current.mediaFileId) return;
      switchingRef.current = true;
      // The viewer picked the version in the player: the new attempt's first
      // frame is timed from here.
      const intentAt = performance.now();

      (async () => {
        try {
          await loadSession({
            preferredFileId: newFileId,
            position: currentPosition,
            forceStartPosition: true,
            // The server retires the old session before it attempts a
            // replacement start. A failed response therefore cannot leave the
            // old plan active on the client.
            allowPreserveExistingSessionOnError: false,
            replacementErrorMessage: "Failed to switch playback version",
            initialErrorMessage: "Failed to switch version",
            intentAt,
          });
        } finally {
          switchingRef.current = false;
        }
      })();
    },
    [allowAlternateVersions, loadSession],
  );

  return {
    ...state,
    switchVersion,
    switchAudioTrack,
    changeSubtitleTrack,
    changeQuality,
    recoverFromFailure,
    recoverConnection,
    retryConnection,
    invalidatePlan,
    reanchorSeek,
    refreshSubtitles,
    applySubtitleTrack,
    updatePlaybackState,
    reportFirstFrame,
    reportEvent,
  };
}

/**
 * Resolves the default file for the variant the viewer is resuming.
 *
 * This is edition/part selection, not adaptation: it answers "which cut of this
 * item" from client-held resume state. Which encode of that cut plays is the
 * server's decision.
 */
function selectDefaultVariantFile(
  playbackVariants: PlayerPlaybackVariant[],
  versions: PlayerFileVersion[],
  resumeHints?: ResumeHints,
): number | null {
  if (playbackVariants.length === 0) {
    return null;
  }

  let candidateVariants = playbackVariants;
  if (
    resumeHints?.lastEditionKey &&
    playbackVariants.some((variant) => variant.edition_key === resumeHints.lastEditionKey)
  ) {
    candidateVariants = playbackVariants.filter(
      (variant) => variant.edition_key === resumeHints.lastEditionKey,
    );
  } else if (playbackVariants.some((variant) => !variant.edition_key)) {
    candidateVariants = playbackVariants.filter((variant) => !variant.edition_key);
  }

  for (const variant of candidateVariants) {
    const firstPart = [...(variant.parts ?? [])].sort((a, b) => a.part_index - b.part_index)[0];
    if (!firstPart) {
      continue;
    }

    if (firstPart.default_file_id != null) {
      const known = versions.find((version) => version.file_id === firstPart.default_file_id);
      if (known) return known.file_id;
    }

    const firstVersion = (firstPart.versions ?? [])[0];
    if (firstVersion) return firstVersion.file_id;
  }

  return null;
}
