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
}

interface TransportRequestResult {
  ok: boolean;
}

interface UseWatchTogetherPlaybackSyncResult {
  attachedSessionId: string | null;
  requestTransport: (
    action: "play" | "pause" | "seek",
    positionSeconds: number,
    isPaused: boolean,
  ) => TransportRequestResult;
  reportReady: (positionSeconds?: number, isPaused?: boolean) => TransportRequestResult;
  reportBuffering: (positionSeconds?: number, isPaused?: boolean) => TransportRequestResult;
}

const stateReportIntervalMs = 1_500;
const pendingCommandQuietPeriodMs = 250;

export function useWatchTogetherPlaybackSync({
  roomConnection,
  sessionId,
  videoRef,
  streamOriginRef,
}: UseWatchTogetherPlaybackSyncOptions): UseWatchTogetherPlaybackSyncResult {
  const connectionState = roomConnection.connectionState;
  const room = roomConnection.room;
  const transportCommand = roomConnection.transportCommand;
  const serverTimeOffsetMs = roomConnection.serverTimeOffsetMs;
  const attachedSessionId = room?.attached_session_id ?? null;
  const roomConnected = room !== null;
  const roomPlaybackState = room?.playback_state ?? null;
  const roomPhase = room?.phase ?? null;
  const sendRoomMessage = roomConnection.sendRoomMessage;
  const waitingStateRef = useRef<"idle" | "buffering" | "ready">("idle");

  useEffect(() => {
    if (!sessionId || attachedSessionId !== sessionId || room?.playback_state !== "waiting") {
      waitingStateRef.current = "idle";
    }
  }, [attachedSessionId, room?.playback_state, room?.selection_revision, sessionId]);

  useEffect(() => {
    if (!sessionId || connectionState !== "connected") {
      return;
    }

    sendRoomMessage({ type: "attach_session", session_id: sessionId });
  }, [connectionState, sendRoomMessage, sessionId]);

  useEffect(() => {
    if (!sessionId || connectionState !== "connected") {
      return;
    }

    const intervalId = window.setInterval(() => {
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

      sendRoomMessage({
        type: "state_report",
        session_id: sessionId,
        position_seconds: toMediaTime(video.currentTime, streamOriginRef.current),
        is_paused: video.paused,
      });
    }, stateReportIntervalMs);

    return () => {
      window.clearInterval(intervalId);
    };
  }, [
    attachedSessionId,
    connectionState,
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

  const reportReady = useCallback(
    (positionSeconds?: number, isPaused?: boolean) => {
      const video = videoRef.current;
      if (
        connectionState !== "connected" ||
        !roomConnected ||
        !sessionId ||
        attachedSessionId !== sessionId ||
        roomPlaybackState !== "waiting" ||
        waitingStateRef.current === "ready" ||
        !video
      ) {
        return { ok: false };
      }

      const result = sendRoomMessage({
        type: "ready",
        session_id: sessionId,
        position_seconds: Math.max(
          0,
          positionSeconds ?? toMediaTime(video.currentTime, streamOriginRef.current),
        ),
        is_paused: isPaused ?? video.paused,
      });
      if (result.ok) {
        waitingStateRef.current = "ready";
      }
      return result;
    },
    [
      attachedSessionId,
      connectionState,
      roomConnected,
      roomPlaybackState,
      sendRoomMessage,
      sessionId,
      streamOriginRef,
      videoRef,
    ],
  );

  const reportBuffering = useCallback(
    (positionSeconds?: number, isPaused?: boolean) => {
      const video = videoRef.current;
      if (
        connectionState !== "connected" ||
        !roomConnected ||
        !sessionId ||
        attachedSessionId !== sessionId ||
        roomPhase !== "playing" ||
        waitingStateRef.current === "buffering" ||
        !video
      ) {
        return { ok: false };
      }

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
      }
      return result;
    },
    [
      attachedSessionId,
      connectionState,
      roomConnected,
      roomPhase,
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
      requestTransport,
      reportReady,
      reportBuffering,
    }),
    [attachedSessionId, requestTransport, reportReady, reportBuffering],
  );
}
