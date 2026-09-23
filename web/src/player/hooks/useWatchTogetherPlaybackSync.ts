import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  type MutableRefObject,
  type RefObject,
} from "react";
import { toMediaTime } from "../utils/mediaTimeline";
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
    }),
    [
      attachedSessionId,
      catchingUp,
      readinessPending,
      requestTransport,
      reportReady,
      reportBuffering,
    ],
  );
}
