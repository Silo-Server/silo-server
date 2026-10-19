import { useCallback, useEffect, useRef, useState } from "react";
import type { WatchTogetherRoomConnectionResult } from "./useWatchTogetherRoomConnection";

/** Latest absolute seek per input window; pause and play remain separate intents. */
export function useWatchTogetherTransportRequests(
  roomConnection: WatchTogetherRoomConnectionResult,
  sessionId: string | null,
) {
  const { connectionState, room, transportCommand, sendRoomMessage } = roomConnection;
  const attachedSessionId = room?.attached_session_id ?? null;
  const roomConnected = room !== null;
  const admissionSupported = roomConnection.workAdmission?.version === 1;
  const [transportDiscarded, setTransportDiscarded] = useState(0);
  const seekWindowRef = useRef<{
    message: Record<string, unknown>;
    timer: ReturnType<typeof setTimeout>;
  } | null>(null);
  const cancelSeekWindow = useCallback(() => {
    if (seekWindowRef.current) clearTimeout(seekWindowRef.current.timer);
    seekWindowRef.current = null;
  }, []);
  useEffect(
    () => () => {
      if (seekWindowRef.current) setTransportDiscarded((value) => value + 1);
      cancelSeekWindow();
    },
    [
      cancelSeekWindow,
      connectionState,
      roomConnection.workAdmission,
      room?.phase,
      room?.room_id,
      room?.selection_revision,
      room?.generation,
      room?.self_can_control_transport,
      room?.self_can_manage_room,
      sessionId,
      attachedSessionId,
      transportCommand?.command_id,
    ],
  );
  const requestTransport = useCallback(
    (action: "play" | "pause" | "seek", positionSeconds: number, isPaused: boolean) => {
      if (
        connectionState !== "connected" ||
        !roomConnected ||
        !sessionId ||
        attachedSessionId !== sessionId ||
        (action === "seek" ? !room?.self_can_manage_room : !room?.self_can_control_transport)
      )
        return { ok: false };
      const message = {
        type: "transport_request",
        ...(admissionSupported ? { session_id: sessionId } : {}),
        action,
        position_seconds: positionSeconds,
        is_paused: isPaused,
      };
      if (action === "seek" && admissionSupported) {
        if (seekWindowRef.current) seekWindowRef.current.message = message;
        else {
          const pending = {
            message,
            timer: setTimeout(() => {
              if (seekWindowRef.current !== pending) return;
              seekWindowRef.current = null;
              if (!sendRoomMessage(pending.message).ok) setTransportDiscarded((value) => value + 1);
            }, 250),
          };
          seekWindowRef.current = pending;
        }
        return { ok: true };
      }
      // A pause/play remains a distinct intent, after an earlier seek.
      if (seekWindowRef.current) {
        const seek = seekWindowRef.current.message;
        cancelSeekWindow();
        sendRoomMessage(seek);
      }
      return sendRoomMessage(message);
    },
    [
      admissionSupported,
      attachedSessionId,
      cancelSeekWindow,
      connectionState,
      room,
      roomConnected,
      sendRoomMessage,
      sessionId,
    ],
  );

  return { requestTransport, transportDiscarded };
}
