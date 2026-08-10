import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import type { PlayerConfig } from "../context/PlayerConfigContext";
import { playerFetch } from "../player-fetch";
import { describePlanTerminal, describePlaybackTransportError } from "../playback-errors";
import { useCodecDetection } from "./useCodecDetection";
import {
  buildClientCapabilitiesV3,
  buildClientPlaybackContextV3,
  detectBandwidthEstimateKbpsV3,
  detectMeteredV3,
} from "../client-context-v3";
import { reportRouteEventV3 } from "../route-events-v3";
import { buildPlayerStreamUrl } from "../stream-url";
import { randomUUID } from "@/lib/uuid";
import {
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
  loading: boolean;
  replacing: boolean;
  replanning: boolean;
  errorTitle: string | null;
  error: string | null;
}

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
  /** `seek_reanchor` replan when the target lies outside the seekable window. */
  reanchorSeek: (positionSeconds: number) => void;
  /** Re-reads the subtitle inventory by replanning with the selection unchanged. */
  refreshSubtitles: (currentPosition: number) => void;
  /** Folds a realtime-delivered inventory entry in without a server round trip. */
  applySubtitleTrack: (track: SubtitleInventoryItemV3) => void;
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
    loading: false,
    replacing: false,
    replanning: false,
    errorTitle: null,
    error: null,
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
): PlaybackSessionErrorState {
  const transportError = describePlaybackTransportError(error);
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
): UsePlaybackSessionResult {
  const config = usePlayerConfig();
  const probe = useCodecDetection();
  const clientCapabilities = useMemo(() => buildClientCapabilitiesV3(probe), [probe]);
  const clientPlaybackContext = useMemo(() => buildClientPlaybackContextV3(probe), [probe]);

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
    loading: true,
    replacing: false,
    replanning: false,
    errorTitle: null,
    error: null,
  });

  const sessionIdRef = useRef<string | null>(null);
  const planRef = useRef<PlanV3 | null>(null);
  const planRevisionRef = useRef(0);
  const stateRef = useRef(state);
  const activeRequestKeyRef = useRef<string | null>(null);
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
  const pendingReplanRef = useRef<{
    options: ReplanOptions;
    loadSequence: number;
  } | null>(null);
  const qualityRef = useRef(qualityPreference?.trim() || "auto");

  useEffect(() => {
    stateRef.current = state;
  }, [state]);

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
      void reportRouteEventV3(config, {
        event,
        playbackAttemptId: attemptId,
        ...routeEventPlanIdentityV3(plan, sessionIdRef.current, planAttemptIdRef.current),
        ...extra,
      });
    },
    [config],
  );

  /**
   * Adopts a decision as the live session, or surfaces its terminal.
   * Returns whether a plan was adopted.
   */
  const adoptDecision = useCallback(
    (decision: DecisionResponseV3): boolean => {
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
          error: failure.message,
        }));
        return false;
      }

      const sessionId = plan.session_id ?? decision.session_id ?? sessionIdRef.current;
      planAttemptIdRef.current = randomUUID();
      planRef.current = plan;
      sessionIdRef.current = sessionId ?? null;
      planRevisionRef.current += 1;

      setState(
        planToSessionState(
          plan,
          sessionId ?? null,
          playbackAttemptIdRef.current ?? "",
          planRevisionRef.current,
          qualityRef.current,
          config,
        ),
      );
      reportEvent("plan_selected");
      return true;
    },
    [config, reportEvent],
  );

  const requestStart = useCallback(
    async (
      targetFileId: number,
      position: number,
      forceStartPosition: boolean,
      playbackAttemptId: string,
    ): Promise<DecisionResponseV3> => {
      const body = buildStartRequestV3({
        fileId: targetFileId,
        profileId: config.getProfileId() ?? "",
        playbackAttemptId,
        qualityPreference: qualityRef.current,
        position,
        forceStartPosition,
        explicitAudioTrackIndex,
        metered: detectMeteredV3(),
        bandwidthEstimateKbps: detectBandwidthEstimateKbpsV3(),
        bandwidthCapKbps: maxBitrateKbps,
        clientCapabilities,
        clientPlaybackContext,
      });

      return playerFetch<DecisionResponseV3>(config, "/playback/start", {
        method: "POST",
        body: JSON.stringify(body),
      });
    },
    [clientCapabilities, clientPlaybackContext, config, explicitAudioTrackIndex, maxBitrateKbps],
  );

  const stopSession = useCallback(
    async (sessionId: string) => {
      await playerFetch(config, `/playback/${sessionId}`, {
        method: "DELETE",
      });
    },
    [config],
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
    }: {
      preferredFileId?: number;
      position: number;
      forceStartPosition: boolean;
      allowPreserveExistingSessionOnError: boolean;
      replacementErrorMessage: string;
      initialErrorMessage: string;
    }) => {
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
        error: hasExistingSession ? current.error : null,
      }));

      // A start begins a new attempt chain: fresh attempt id, empty loop guard.
      const playbackAttemptId = randomUUID();
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

      try {
        const selectedFileId = selectFileId(preferredFileId);
        if (!selectedFileId) {
          throw new Error("No playable version found");
        }

        const decision = await requestStart(
          selectedFileId,
          position,
          forceStartPosition,
          playbackAttemptId,
        );

        if (loadSequence !== loadSequenceRef.current) {
          const staleSessionId = decision.playback_plan?.session_id ?? decision.session_id;
          if (staleSessionId) {
            await stopSession(staleSessionId).catch(() => {
              // Best effort cleanup for stale session starts.
            });
          }
          return;
        }

        const adopted = adoptDecision(decision);
        if (!adopted && hasExistingSession && allowPreserveExistingSessionOnError) {
          restorePreviousAttempt();
          setState((current) => ({
            ...current,
            loading: false,
            replacing: false,
            errorTitle: previousState.errorTitle,
            error: previousState.error,
          }));
          return;
        }
        if (!adopted) {
          retirePreviousSession();
          return;
        }
        if (adopted && previousSessionId && previousSessionId !== sessionIdRef.current) {
          void stopSession(previousSessionId).catch(() => {
            // Best effort — stale session will time out server-side.
          });
        }
      } catch (err) {
        if (loadSequence !== loadSequenceRef.current) {
          return;
        }

        if (hasExistingSession && allowPreserveExistingSessionOnError) {
          console.error(replacementErrorMessage, err);
          restorePreviousAttempt();
          setState((current) => ({
            ...current,
            loading: false,
            replacing: false,
          }));
          return;
        }

        const nextError = describePlaybackSessionError(err, initialErrorMessage);
        retirePreviousSession(nextError);
      }
    },
    [adoptDecision, requestStart, selectFileId, stopSession],
  );

  useEffect(() => {
    if (activeRequestKeyRef.current === requestKey) {
      return;
    }
    activeRequestKeyRef.current = requestKey;
    qualityRef.current = qualityPreference?.trim() || "auto";

    void loadSession({
      preferredFileId: fileId,
      position: initialPosition,
      forceStartPosition: forceInitialPosition,
      allowPreserveExistingSessionOnError: false,
      replacementErrorMessage: "Failed to replace playback request",
      initialErrorMessage: "Failed to start playback",
    });
  }, [fileId, forceInitialPosition, initialPosition, loadSession, qualityPreference, requestKey]);

  // Clean up session on unmount.
  useEffect(() => {
    return () => {
      const sid = sessionIdRef.current;
      if (!sid) return;

      const token = config.getAccessToken();
      const profileId = config.getProfileId();
      const url = `${config.apiBaseUrl}/playback/${sid}`;

      const headers: Record<string, string> = {};
      if (token) headers["Authorization"] = `Bearer ${token}`;
      if (profileId) headers["X-Profile-Id"] = profileId;
      const profileToken = config.getProfileToken?.();
      if (profileToken) headers["X-Profile-Token"] = profileToken;

      // sendBeacon doesn't support DELETE, so use fetch with keepalive.
      fetch(url, {
        method: "DELETE",
        headers,
        keepalive: true,
      }).catch(() => {
        // Best effort — if fetch fails, session will time out server-side.
      });
    };
  }, [config]);

  /**
   * Issues one replan and adopts whatever plan comes back.
   *
   * Replans are serialized server-side behind a lease, so a second concurrent
   * request would earn a `409 replan_in_progress`; the in-flight guard here
   * means the client never asks for one.
   */
  const replan = useCallback(
    async function issueReplan(options: ReplanOptions): Promise<boolean> {
      const plan = planRef.current;
      const sessionId = sessionIdRef.current;
      const playbackAttemptId = playbackAttemptIdRef.current;
      if (!plan || !sessionId || !playbackAttemptId) return false;
      if (replanInFlightRef.current) {
        const isPendingFailureRecovery =
          options.operation === "failure_recovery" || options.operation === "seek_failure_recovery";
        const queuedOperation = pendingReplanRef.current?.options.operation;
        const hasQueuedFailureRecovery =
          queuedOperation === "failure_recovery" || queuedOperation === "seek_failure_recovery";
        if (
          isPendingFailureRecovery ||
          (options.operation === "seek_reanchor" && !hasQueuedFailureRecovery)
        ) {
          pendingReplanRef.current = {
            options,
            loadSequence: loadSequenceRef.current,
          };
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

      // On a track or quality change nothing failed, so the previous route
      // stays eligible: the loop guard and the recovery counter reset. Only a
      // recovery accumulates them.
      const attemptedPlanKeys = isFailureRecovery
        ? [...attemptedPlanKeysRef.current, plan.plan_attempt_key].slice(
            -MAX_ATTEMPTED_PLAN_KEYS_V3,
          )
        : [];
      const attemptCount = isFailureRecovery ? attemptCountRef.current : 1;

      const body = buildReplanRequestV3({
        ...options,
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
      replanInFlightRef.current = true;
      setState((current) => ({
        ...current,
        replanning: true,
        errorTitle: null,
        error: null,
      }));

      try {
        const decision = await playerFetch<DecisionResponseV3>(
          config,
          `/playback/${sessionId}/replan`,
          { method: "POST", body: JSON.stringify(body) },
        );

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

        return adoptDecision(decision);
      } catch (err) {
        if (loadSequence !== loadSequenceRef.current) return false;
        const nextError = describePlaybackSessionError(err, "Failed to update playback");
        setState((current) => ({
          ...current,
          replanning: false,
          errorTitle: nextError.title,
          error: nextError.message,
        }));
        return false;
      } finally {
        replanInFlightRef.current = false;
        setState((current) => (current.replanning ? { ...current, replanning: false } : current));

        const pendingReplan = pendingReplanRef.current;
        pendingReplanRef.current = null;
        if (pendingReplan?.loadSequence === loadSequenceRef.current) {
          void issueReplan(pendingReplan.options);
        }
      }
    },
    [adoptDecision, clientCapabilities, clientPlaybackContext, config, maxBitrateKbps],
  );

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
      reportEvent("plan_failed", {
        failureClassification: failure.classification,
        ...(failure.message ? { diagnostics: { message: failure.message } } : {}),
      });
      void replan({ operation: "failure_recovery", positionSeconds: currentPosition, failure });
    },
    [replan, reportEvent],
  );

  const reanchorSeek = useCallback(
    (positionSeconds: number) => {
      reportEvent("seek_reanchor_requested");
      void replan({ operation: "seek_reanchor", positionSeconds });
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

  const switchVersion = useCallback(
    (newFileId: number, currentPosition: number) => {
      if (switchingRef.current) return;
      if (newFileId === stateRef.current.mediaFileId) return;
      switchingRef.current = true;

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
          });
        } finally {
          switchingRef.current = false;
        }
      })();
    },
    [loadSession],
  );

  return {
    ...state,
    switchVersion,
    switchAudioTrack,
    changeSubtitleTrack,
    changeQuality,
    recoverFromFailure,
    reanchorSeek,
    refreshSubtitles,
    applySubtitleTrack,
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
