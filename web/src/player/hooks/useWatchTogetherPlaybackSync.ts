import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  type MutableRefObject,
  type RefObject,
} from "react";
import { toMediaTime, toPlayerTime } from "../utils/mediaTimeline";
import { isNativePositionInRanges } from "../utils/roomSyncCatchup";
import type { WatchTogetherRoomConnectionResult } from "./useWatchTogetherRoomConnection";

interface UseWatchTogetherPlaybackSyncOptions {
  roomConnection: WatchTogetherRoomConnectionResult;
  sessionId?: string | null;
  videoRef: RefObject<HTMLVideoElement | null>;
  streamOriginRef: MutableRefObject<number>;
  appliedCommandIdRef: RefObject<string | null>;
  /** Called when a sustained stall is reported to the room. */
  onSustainedStall?: () => void;
}

interface TransportRequestResult {
  ok: boolean;
}

interface UseWatchTogetherPlaybackSyncResult {
  attachedSessionId: string | null;
  /**
   * The room kept going without this viewer, who catches up on its own and
   * must acknowledge recovery before the room counts it as ready again.
   */
  catchingUp: boolean;
  /** A readiness acknowledgement is due: the room waits, or this viewer recovers. */
  readinessPending: boolean;
  requestTransport: (
    action: "play" | "pause" | "seek",
    positionSeconds: number,
    isPaused: boolean,
  ) => TransportRequestResult;
  reportReady: () => TransportRequestResult;
  reportBuffering: (positionSeconds?: number, isPaused?: boolean) => TransportRequestResult;
  /**
   * While the element plays a stream's pre-roll up to a room seek target it
   * is muted for the pre-roll, not by the viewer. This is the viewer's mute
   * setting then, restored when the pre-roll ends; null when no pre-roll runs.
   */
  prerollMutedPreference: () => boolean | null;
  /** Records the viewer's mute choice during a pre-roll. False when none runs. */
  setPrerollMutedPreference: (muted: boolean) => boolean;
}

const stateReportIntervalMs = 1_500;
// Retry readiness until the server acknowledges this member, so a lost or
// rejected acknowledgement heals quickly while another member may still load.
const waitingReportIntervalMs = 500;
const pendingCommandQuietPeriodMs = 250;
const readySeekToleranceSeconds = 1;
// The host's real position becomes the room anchor, so a rebuilt stream that
// lands short of the target does not hold the room. Mirrors the server bound.
const hostReadySeekToleranceSeconds = 15;
// Stalls shorter than the room catch-up band stay local: the viewer converges
// by playback rate instead of pausing everyone.
const bufferingGraceMs = 2_000;
// A rebuilt stream can begin short of a room seek target: a copy remux starts
// at the preceding keyframe, and a progressive response cannot seek inside
// itself, so the plan expects the player to play through that pre-roll. Only
// a pre-roll at the start of the stream, and no longer than this, is played.
const maxPrerollSeconds = 20;
// The pre-roll plays muted behind the syncing overlay, so it can run fast.
// Close to the target it drops to normal speed, which leaves a late check the
// most room: the stream cannot seek back to a target it has passed.
const prerollPlaybackRate = 4;
const prerollFinalApproachSeconds = 1.5;
const prerollCheckIntervalMs = 50;
// Nothing in the element stops it at a position; only a check on the main
// thread can, and checks are as regular as that thread allows. A hidden tab
// throttles timers to about one a second, so the estimate of how late the next
// check may be starts there and then follows the delays actually seen. Both
// the rate and the stopping point answer to it.
const visiblePrerollWakeupSeconds = 0.3;
const hiddenPrerollWakeupSeconds = 1.5;

/** How late the next check may be, given this tab and the delays seen so far. */
function prerollWakeupSeconds(observedSeconds: number): number {
  const floor =
    document.visibilityState === "visible"
      ? visiblePrerollWakeupSeconds
      : hiddenPrerollWakeupSeconds;
  return Math.max(floor, observedSeconds);
}

/** Playback rate for a pre-roll this far short of the room's seek target. */
function prerollRateFor(remainingSeconds: number, wakeupSeconds: number): number {
  if (remainingSeconds <= prerollFinalApproachSeconds) return 1;
  return Math.min(prerollPlaybackRate, Math.max(1, remainingSeconds / wakeupSeconds));
}

/**
 * Whether to stop the pre-roll here. Stopping short of the target is safe: the
 * room acknowledges a position inside its tolerance and absorbs the rest by
 * rate. Stopping past it is not, so the pre-roll gives up the last of the gap
 * once a late check could carry the element out of that tolerance.
 */
function prerollLanded(
  remainingSeconds: number,
  rate: number,
  wakeupSeconds: number,
  toleranceSeconds: number,
): boolean {
  if (remainingSeconds <= 0) return true;
  return (
    remainingSeconds <= toleranceSeconds &&
    rate * wakeupSeconds > remainingSeconds + toleranceSeconds
  );
}

type ReadyCheck =
  | { ok: true; commandId: string; positionSeconds: number; isPaused: boolean }
  | { ok: false; reason: string };

export function useWatchTogetherPlaybackSync({
  roomConnection,
  sessionId,
  videoRef,
  streamOriginRef,
  appliedCommandIdRef,
  onSustainedStall,
}: UseWatchTogetherPlaybackSyncOptions): UseWatchTogetherPlaybackSyncResult {
  const connectionState = roomConnection.connectionState;
  const room = roomConnection.room;
  const transportCommand = roomConnection.transportCommand;
  const serverTimeOffsetMs = roomConnection.serverTimeOffsetMs;
  const attachedSessionId = room?.attached_session_id ?? null;
  const roomConnected = room !== null;
  const roomPlaybackState = room?.playback_state ?? null;
  const roomPhase = room?.phase ?? null;
  const roomSelectionRevision = room?.selection_revision;
  const isHost = room?.self_role === "host";
  const selfMember = room?.members?.find((member) => member.is_self);
  // The server clears readiness in its snapshot before each waiting command.
  const readinessAcknowledged = selfMember?.is_ready === true;
  // The room resumed without this viewer (its waiting deadline passed, or the
  // room does not wait for this viewer's stalls). Recovery must still be
  // acknowledged, or the server keeps the viewer marked buffering and never
  // sends it a fresh target.
  const catchingUp =
    roomPhase === "playing" &&
    (roomPlaybackState === "playing" || roomPlaybackState === "paused") &&
    (room?.self_ignore_wait === true || selfMember?.is_buffering === true);
  const readinessPending =
    (roomPlaybackState === "waiting" || catchingUp) && !readinessAcknowledged;
  const waitingSeekCommandId =
    roomPlaybackState === "waiting" && transportCommand?.action === "seek"
      ? transportCommand.command_id
      : null;
  const lastReadyRejectReasonRef = useRef<string | null>(null);
  const sendRoomMessage = roomConnection.sendRoomMessage;
  const waitingStateRef = useRef<"idle" | "buffering" | "ready">("idle");
  const bufferingTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const cancelBuffering = useCallback(() => {
    if (bufferingTimerRef.current !== null) {
      clearTimeout(bufferingTimerRef.current);
      bufferingTimerRef.current = null;
    }
  }, []);
  useEffect(() => {
    const video = videoRef.current;
    const recovered = () => {
      if (video && !video.seeking && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) {
        cancelBuffering();
        if (roomPlaybackState !== "waiting" && waitingStateRef.current === "buffering") {
          waitingStateRef.current = "idle";
        }
      }
    };
    video?.addEventListener("canplay", recovered);
    video?.addEventListener("canplaythrough", recovered);
    video?.addEventListener("loadeddata", recovered);
    video?.addEventListener("playing", recovered);
    video?.addEventListener("timeupdate", recovered);
    video?.addEventListener("seeked", recovered);
    return () => {
      cancelBuffering();
      video?.removeEventListener("canplay", recovered);
      video?.removeEventListener("canplaythrough", recovered);
      video?.removeEventListener("loadeddata", recovered);
      video?.removeEventListener("playing", recovered);
      video?.removeEventListener("timeupdate", recovered);
      video?.removeEventListener("seeked", recovered);
    };
  }, [
    cancelBuffering,
    connectionState,
    attachedSessionId,
    roomPhase,
    roomPlaybackState,
    room?.room_id,
    room?.selection_revision,
    sessionId,
    transportCommand?.command_id,
    videoRef,
  ]);

  // A waiting room holds this viewer paused, so a stream that begins in the
  // pre-roll before the seek target would never reach it and the room would
  // wait out its deadline. Play the pre-roll through, muted and fast, and stop
  // at the target; the readiness check then acknowledges the seek.
  const prerollRef = useRef<{
    commandId: string;
    restoreMuted: boolean;
    restoreRate: number;
    /** When the pre-roll was last checked, to measure how late checks run. */
    lastCheckMs: number;
  } | null>(null);
  // The command whose seek rebuilt the current stream. Until the rebuilt
  // stream loads, the element still holds the stream the seek replaces.
  const rebuiltForCommandRef = useRef<string | null>(null);
  const endPreroll = useCallback(
    (pause: boolean) => {
      const preroll = prerollRef.current;
      prerollRef.current = null;
      const video = videoRef.current;
      if (!preroll || !video) return;
      if (pause) video.pause();
      video.muted = preroll.restoreMuted;
      video.playbackRate = preroll.restoreRate;
    },
    [videoRef],
  );
  const advanceThroughPreroll = useCallback(
    (video: HTMLVideoElement) => {
      const command = transportCommand;
      if (
        prerollRef.current ||
        !command ||
        command.command_id !== waitingSeekCommandId ||
        appliedCommandIdRef.current !== command.command_id ||
        rebuiltForCommandRef.current !== command.command_id ||
        !video.paused ||
        video.seeking ||
        video.readyState < HTMLMediaElement.HAVE_CURRENT_DATA ||
        video.currentTime > maxPrerollSeconds
      ) {
        return;
      }
      const origin = streamOriginRef.current;
      const gap = command.position_seconds - toMediaTime(video.currentTime, origin);
      const tolerance = isHost ? hostReadySeekToleranceSeconds : readySeekToleranceSeconds;
      if (gap <= tolerance || gap > maxPrerollSeconds) return;
      // A target the element can seek to does not need the pre-roll.
      if (
        isNativePositionInRanges(video.seekable, toPlayerTime(command.position_seconds, origin))
      ) {
        return;
      }
      const preroll = {
        commandId: command.command_id,
        restoreMuted: video.muted,
        restoreRate: video.playbackRate,
        lastCheckMs: performance.now(),
      };
      prerollRef.current = preroll;
      video.muted = true;
      video.playbackRate = prerollRateFor(gap, prerollWakeupSeconds(0));
      video.play().catch(() => {
        // A later pre-roll owns the element now; leave it alone.
        if (prerollRef.current === preroll) endPreroll(false);
      });
    },
    [
      appliedCommandIdRef,
      endPreroll,
      isHost,
      streamOriginRef,
      transportCommand,
      waitingSeekCommandId,
    ],
  );
  useEffect(() => {
    const video = videoRef.current;
    const preroll = prerollRef.current;
    // Stop before restoring audio. A new command takes over playback at its
    // scheduled execution time, which may still be in the future.
    if (preroll && preroll.commandId !== waitingSeekCommandId) {
      endPreroll(true);
    }
    if (!video || !waitingSeekCommandId) return;
    const targetSeconds = transportCommand?.position_seconds ?? 0;
    const tolerance = isHost ? hostReadySeekToleranceSeconds : readySeekToleranceSeconds;
    const checkProgress = () => {
      const preroll = prerollRef.current;
      if (preroll?.commandId !== waitingSeekCommandId) return;
      const nowMs = performance.now();
      const wakeup = prerollWakeupSeconds((nowMs - preroll.lastCheckMs) / 1000);
      preroll.lastCheckMs = nowMs;
      const remaining = targetSeconds - toMediaTime(video.currentTime, streamOriginRef.current);
      const rate = prerollRateFor(remaining, wakeup);
      if (prerollLanded(remaining, rate, wakeup, tolerance)) {
        endPreroll(true);
        return;
      }
      if (video.playbackRate !== rate) video.playbackRate = rate;
    };
    // timeupdate may come only every 250 ms, so poll as well.
    const intervalId = window.setInterval(checkProgress, prerollCheckIntervalMs);
    // A stream replaced mid-pre-roll starts over from its own position.
    const onEmptied = () => endPreroll(false);
    const onLoadStart = () => {
      if (appliedCommandIdRef.current === waitingSeekCommandId) {
        rebuiltForCommandRef.current = waitingSeekCommandId;
      }
    };
    video.addEventListener("timeupdate", checkProgress);
    video.addEventListener("emptied", onEmptied);
    video.addEventListener("loadstart", onLoadStart);
    document.addEventListener("visibilitychange", checkProgress);
    return () => {
      window.clearInterval(intervalId);
      document.removeEventListener("visibilitychange", checkProgress);
      video.removeEventListener("timeupdate", checkProgress);
      video.removeEventListener("emptied", onEmptied);
      video.removeEventListener("loadstart", onLoadStart);
    };
  }, [
    appliedCommandIdRef,
    endPreroll,
    isHost,
    streamOriginRef,
    transportCommand?.position_seconds,
    videoRef,
    waitingSeekCommandId,
  ]);
  useEffect(() => () => endPreroll(false), [endPreroll]);
  const prerollMutedPreference = useCallback(() => prerollRef.current?.restoreMuted ?? null, []);
  const setPrerollMutedPreference = useCallback((muted: boolean) => {
    const preroll = prerollRef.current;
    if (!preroll) return false;
    preroll.restoreMuted = muted;
    return true;
  }, []);

  // A new stream, room, selection, phase, or connection starts over.
  useEffect(() => {
    waitingStateRef.current = "idle";
  }, [
    attachedSessionId,
    connectionState,
    roomPhase,
    room?.room_id,
    room?.selection_revision,
    sessionId,
  ]);

  // A new command or readiness reset calls for a fresh acknowledgement. A
  // reported stall stays reported until the media recovers: the snapshot that
  // marks this viewer buffering must not let a later event for the same
  // outage report it again.
  useEffect(() => {
    if (waitingStateRef.current === "ready") waitingStateRef.current = "idle";
  }, [readinessAcknowledged, room?.playback_state, transportCommand?.command_id]);

  useEffect(() => {
    if (!sessionId || connectionState !== "connected") {
      return;
    }

    sendRoomMessage({ type: "attach_session", session_id: sessionId });
  }, [connectionState, sendRoomMessage, sessionId]);

  // Level-triggered readiness: anything that can acknowledge the waiting
  // command evaluates the same guards, so no single media event is the one
  // chance to leave the barrier.
  const checkReady = useCallback((): ReadyCheck => {
    const video = videoRef.current;
    const command = transportCommand;
    if (connectionState !== "connected" || !roomConnected) {
      return { ok: false, reason: "room not connected" };
    }
    if (!sessionId || attachedSessionId !== sessionId) {
      return { ok: false, reason: "playback session not attached" };
    }
    if (roomPlaybackState !== "waiting" && !catchingUp) {
      return { ok: false, reason: "room is not waiting" };
    }
    if (!command || command.playback_state !== roomPlaybackState) {
      return { ok: false, reason: "no command for the room's playback state" };
    }
    if (command.selection_revision !== roomSelectionRevision) {
      return { ok: false, reason: "command belongs to a previous selection" };
    }
    if (command.session_id && command.session_id !== sessionId) {
      return { ok: false, reason: "command targets another session" };
    }
    if (appliedCommandIdRef.current !== command.command_id) {
      return { ok: false, reason: "command not yet executed locally" };
    }
    if (!video) {
      return { ok: false, reason: "no media element" };
    }
    if (video.seeking) {
      return { ok: false, reason: "element still seeking" };
    }
    if (prerollRef.current) {
      return { ok: false, reason: "playing through the stream pre-roll" };
    }
    if (video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA) {
      return { ok: false, reason: `element readyState ${video.readyState} < HAVE_FUTURE_DATA` };
    }
    const positionSeconds = Math.max(0, toMediaTime(video.currentTime, streamOriginRef.current));
    // A canplay event can still belong to the stream a room seek replaces.
    if (roomPlaybackState === "waiting" && command.action === "seek") {
      const delta = Math.abs(positionSeconds - command.position_seconds);
      const tolerance = isHost ? hostReadySeekToleranceSeconds : readySeekToleranceSeconds;
      if (delta > tolerance) {
        return {
          ok: false,
          reason: `position ${positionSeconds.toFixed(2)}s is ${delta.toFixed(2)}s from seek target ${command.position_seconds.toFixed(2)}s`,
        };
      }
    }
    return { ok: true, commandId: command.command_id, positionSeconds, isPaused: video.paused };
  }, [
    appliedCommandIdRef,
    attachedSessionId,
    catchingUp,
    connectionState,
    isHost,
    roomConnected,
    roomPlaybackState,
    roomSelectionRevision,
    sessionId,
    streamOriginRef,
    transportCommand,
    videoRef,
  ]);

  const noteReadyReject = useCallback(
    (reason: string) => {
      if (lastReadyRejectReasonRef.current === reason) return;
      lastReadyRejectReasonRef.current = reason;
      console.debug(
        `[watch-together] not ready for command ${transportCommand?.command_id ?? "?"}: ${reason}`,
      );
    },
    [transportCommand?.command_id],
  );

  useEffect(() => {
    lastReadyRejectReasonRef.current = null;
  }, [transportCommand?.command_id, roomPlaybackState]);

  useEffect(() => {
    if (!sessionId || connectionState !== "connected") {
      return;
    }

    const retryReadiness = readinessPending;
    const intervalId = window.setInterval(
      () => {
        const video = videoRef.current;
        if (!video || attachedSessionId !== sessionId) {
          return;
        }
        if (transportCommand?.session_id === sessionId) {
          const localExecuteAt = Date.parse(transportCommand.execute_at) - serverTimeOffsetMs;
          if (
            Number.isFinite(localExecuteAt) &&
            localExecuteAt + pendingCommandQuietPeriodMs > Date.now()
          ) {
            return;
          }
        }

        if (retryReadiness) {
          const check = checkReady();
          if (check.ok) {
            // A waiting room also accepts readiness on the state tick; a room
            // that resumed without this viewer needs an explicit ready.
            sendRoomMessage({
              type: catchingUp ? "ready" : "state_report",
              session_id: sessionId,
              command_id: check.commandId,
              position_seconds: check.positionSeconds,
              is_paused: check.isPaused,
              is_ready: true,
            });
            return;
          }
          noteReadyReject(check.reason);
          advanceThroughPreroll(video);
        }

        // A stalled element reports where it stopped, not a decision. Stay
        // quiet until it plays again so the room neither corrects nor follows
        // a stream that cannot move. While recovery is pending the same holds
        // when paused: the server would take a report that matches the room
        // as recovery, and only the guarded ready above may end it.
        if (
          (!video.paused || retryReadiness) &&
          video.readyState < HTMLMediaElement.HAVE_FUTURE_DATA
        ) {
          return;
        }

        sendRoomMessage({
          type: "state_report",
          session_id: sessionId,
          position_seconds: toMediaTime(video.currentTime, streamOriginRef.current),
          is_paused: video.paused,
        });
      },
      retryReadiness ? waitingReportIntervalMs : stateReportIntervalMs,
    );

    return () => {
      window.clearInterval(intervalId);
    };
  }, [
    advanceThroughPreroll,
    attachedSessionId,
    catchingUp,
    checkReady,
    connectionState,
    noteReadyReject,
    readinessPending,
    sendRoomMessage,
    serverTimeOffsetMs,
    sessionId,
    streamOriginRef,
    transportCommand,
    videoRef,
  ]);

  const requestTransport = useCallback(
    (action: "play" | "pause" | "seek", positionSeconds: number, isPaused: boolean) => {
      if (
        connectionState !== "connected" ||
        !roomConnected ||
        !sessionId ||
        attachedSessionId !== sessionId
      ) {
        return { ok: false };
      }
      return sendRoomMessage({
        type: "transport_request",
        action,
        position_seconds: positionSeconds,
        is_paused: isPaused,
      });
    },
    [attachedSessionId, connectionState, roomConnected, sendRoomMessage, sessionId],
  );

  const reportReady = useCallback(() => {
    cancelBuffering();
    if (readinessAcknowledged || waitingStateRef.current === "ready") {
      return { ok: false };
    }
    const check = checkReady();
    if (!check.ok) {
      noteReadyReject(check.reason);
      return { ok: false };
    }
    const result = sendRoomMessage({
      type: "ready",
      command_id: check.commandId,
      session_id: sessionId,
      position_seconds: check.positionSeconds,
      is_paused: check.isPaused,
    });
    if (result.ok) {
      waitingStateRef.current = "ready";
    }
    return result;
  }, [
    cancelBuffering,
    checkReady,
    noteReadyReject,
    readinessAcknowledged,
    sendRoomMessage,
    sessionId,
  ]);

  const reportBuffering = useCallback(
    (positionSeconds?: number, isPaused?: boolean) => {
      const video = videoRef.current;
      if (
        connectionState !== "connected" ||
        !roomConnected ||
        !sessionId ||
        attachedSessionId !== sessionId ||
        roomPhase !== "playing" ||
        roomPlaybackState !== "playing" ||
        waitingStateRef.current === "buffering" ||
        !video
      ) {
        return { ok: false };
      }

      if (bufferingTimerRef.current !== null) return { ok: false };
      bufferingTimerRef.current = setTimeout(() => {
        bufferingTimerRef.current = null;
        // A stalled download can leave plenty of playable media buffered.
        if (!video.seeking && video.readyState >= HTMLMediaElement.HAVE_FUTURE_DATA) return;
        const result = sendRoomMessage({
          type: "buffering",
          session_id: sessionId,
          position_seconds: Math.max(
            0,
            positionSeconds ?? toMediaTime(video.currentTime, streamOriginRef.current),
          ),
          is_paused: isPaused ?? video.paused,
        });
        if (result.ok) {
          waitingStateRef.current = "buffering";
          onSustainedStall?.();
        }
      }, bufferingGraceMs);
      return { ok: true };
    },
    [
      attachedSessionId,
      connectionState,
      onSustainedStall,
      roomConnected,
      roomPhase,
      roomPlaybackState,
      sendRoomMessage,
      sessionId,
      streamOriginRef,
      videoRef,
    ],
  );

  // Stable identity so consumers (e.g. VideoPlayer's video-event-listener
  // effect) don't re-run on every room snapshot.
  return useMemo(
    () => ({
      attachedSessionId,
      catchingUp,
      readinessPending,
      requestTransport,
      reportReady,
      reportBuffering,
      prerollMutedPreference,
      setPrerollMutedPreference,
    }),
    [
      attachedSessionId,
      catchingUp,
      readinessPending,
      requestTransport,
      reportReady,
      reportBuffering,
      prerollMutedPreference,
      setPrerollMutedPreference,
    ],
  );
}
