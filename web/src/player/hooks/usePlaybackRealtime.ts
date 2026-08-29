import { useEffect, useRef, useState } from "react";
import { usePlayerConfig } from "../context/PlayerConfigContext";
import {
  buildPlaybackRealtimeAck,
  buildPlaybackRealtimeHello,
  buildPlaybackRealtimeResult,
  parsePlaybackRealtimeMessage,
  type PlaybackCommandName,
  type PlaybackRealtimeCommandEnvelope,
  type PlaybackRealtimeEventEnvelope,
} from "../realtime-protocol";

type ConnectionState = "disconnected" | "connecting" | "connected";

interface UsePlaybackRealtimeOptions {
  sessionId: string | null;
  onCommand: (command: PlaybackRealtimeCommandEnvelope) => Promise<void> | void;
  onEvent?: (event: PlaybackRealtimeEventEnvelope) => void;
  /**
   * The commands this surface can execute, announced in the hello. Defaults to
   * the shared set; a surface that handles more names them so it does not
   * announce a command it would only reject.
   */
  supportedCommands?: PlaybackCommandName[];
}

interface UsePlaybackRealtimeResult {
  connectionState: ConnectionState;
}

const reconnectDelays = [500, 1_000, 2_000, 5_000];

export function createPlaybackRealtimeUrlFactory(
  apiBaseUrl: string,
  sessionId: string,
  getAccessToken: () => string | null,
): () => string {
  const wsBase = apiBaseUrl.replace(/^http/, "ws");
  return () => {
    const token = getAccessToken();
    return `${wsBase}/playback/sessions/${sessionId}/control/ws${token ? `?token=${token}` : ""}`;
  };
}

export function usePlaybackRealtime({
  sessionId,
  onCommand,
  onEvent,
  supportedCommands,
}: UsePlaybackRealtimeOptions): UsePlaybackRealtimeResult {
  const config = usePlayerConfig();
  const [connectionState, setConnectionState] = useState<ConnectionState>("disconnected");
  const onCommandRef = useRef(onCommand);
  const onEventRef = useRef(onEvent);
  const supportedCommandsRef = useRef(supportedCommands);
  const seenCommandsRef = useRef<Set<string>>(new Set());

  useEffect(() => {
    onCommandRef.current = onCommand;
  }, [onCommand]);

  useEffect(() => {
    supportedCommandsRef.current = supportedCommands;
  }, [supportedCommands]);

  useEffect(() => {
    onEventRef.current = onEvent;
  }, [onEvent]);

  useEffect(() => {
    if (!sessionId) {
      seenCommandsRef.current.clear();
      return;
    }

    const getWsUrl = createPlaybackRealtimeUrlFactory(
      config.apiBaseUrl,
      sessionId,
      config.getAccessToken,
    );

    let disposed = false;
    let attempt = 0;
    let socket: WebSocket | null = null;
    let reconnectTimer: number | null = null;

    const scheduleReconnect = () => {
      if (disposed) return;
      const delay = reconnectDelays[Math.min(attempt, reconnectDelays.length - 1)];
      attempt += 1;
      reconnectTimer = window.setTimeout(connect, delay);
    };

    const connect = () => {
      if (disposed) return;
      setConnectionState("connecting");

      try {
        socket = new WebSocket(getWsUrl());
      } catch {
        scheduleReconnect();
        return;
      }

      socket.addEventListener("open", () => {
        if (!socket || socket.readyState !== WebSocket.OPEN) return;
        attempt = 0;
        setConnectionState("connected");
        seenCommandsRef.current.clear();
        socket.send(
          JSON.stringify(buildPlaybackRealtimeHello(sessionId, supportedCommandsRef.current)),
        );
      });

      socket.addEventListener("message", (event) => {
        const message = parsePlaybackRealtimeMessage(String(event.data));
        if (!message || message.session_id !== sessionId || !socket) {
          return;
        }
        if (message.type === "event") {
          onEventRef.current?.(message);
          return;
        }

        const command = message;
        if (seenCommandsRef.current.has(command.command_id)) {
          return;
        }
        seenCommandsRef.current.add(command.command_id);

        if (socket.readyState === WebSocket.OPEN) {
          socket.send(JSON.stringify(buildPlaybackRealtimeAck(sessionId, command.command_id)));
        }

        void Promise.resolve(onCommandRef.current(command))
          .then(() => {
            if (!socket || socket.readyState !== WebSocket.OPEN) return;
            socket.send(
              JSON.stringify(
                buildPlaybackRealtimeResult(sessionId, command.command_id, "completed"),
              ),
            );
          })
          .catch((error: unknown) => {
            if (!socket || socket.readyState !== WebSocket.OPEN) return;
            const message = error instanceof Error ? error.message : "command_failed";
            socket.send(
              JSON.stringify(
                buildPlaybackRealtimeResult(sessionId, command.command_id, "rejected", message),
              ),
            );
          });
      });

      socket.addEventListener("close", () => {
        setConnectionState("disconnected");
        socket = null;
        scheduleReconnect();
      });

      socket.addEventListener("error", () => {
        socket?.close();
      });
    };

    connect();

    return () => {
      disposed = true;
      setConnectionState("disconnected");
      if (reconnectTimer !== null) {
        window.clearTimeout(reconnectTimer);
      }
      if (
        socket &&
        (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)
      ) {
        socket.close();
      }
    };
  }, [config, sessionId]);

  return { connectionState };
}
