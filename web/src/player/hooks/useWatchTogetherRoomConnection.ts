import { useCallback, useEffect, useRef, useState } from "react";
import { ApiClientError, getAccessToken, getProfileToken } from "@/api/client";
import {
  closeWatchTogetherRoom,
  type CreateWatchTogetherSuggestionInput,
  createWatchTogetherSuggestion,
  deleteWatchTogetherSuggestion,
  type GuestControlPolicy,
  getWatchTogetherRoom,
  listWatchTogetherSuggestions,
  promoteWatchTogetherSuggestion,
  selectWatchTogetherRoomItem,
  type SelectWatchTogetherRoomItemInput,
  unvoteWatchTogetherSuggestion,
  updateWatchTogetherRoomPolicy,
  voteWatchTogetherSuggestion,
  type WatchTogetherTransportCommand,
  type WatchTogetherRoomSnapshot,
  type WatchTogetherSuggestion,
} from "@/lib/watchTogether";
import { storage } from "@/utils/storage";

export type WatchTogetherConnectionState = "disconnected" | "connecting" | "connected";

interface UseWatchTogetherRoomConnectionOptions {
  roomId?: string | null;
  roomToken?: string | null;
}

interface SendRoomMessageResult {
  ok: boolean;
}

export interface WatchTogetherRoomConnectionResult {
  connectionState: WatchTogetherConnectionState;
  room: WatchTogetherRoomSnapshot | null;
  suggestions: WatchTogetherSuggestion[];
  closedReason: string | null;
  transportCommand: WatchTogetherTransportCommand | null;
  serverTimeOffsetMs: number;
  sendRoomMessage: (message: Record<string, unknown>) => SendRoomMessageResult;
  updatePolicy: (policy: GuestControlPolicy) => Promise<WatchTogetherRoomSnapshot | null>;
  selectItem: (
    input: SelectWatchTogetherRoomItemInput,
  ) => Promise<WatchTogetherRoomSnapshot | null>;
  closeRoom: () => Promise<void>;
  createSuggestion: (input: CreateWatchTogetherSuggestionInput) => Promise<void>;
  deleteSuggestion: (suggestionId: string) => Promise<void>;
  vote: (suggestionId: string) => Promise<void>;
  unvote: (suggestionId: string) => Promise<void>;
  promoteSuggestion: (suggestionId: string) => Promise<WatchTogetherRoomSnapshot | null>;
}

const reconnectDelays = [500, 1_000, 2_000, 5_000];
const pingIntervalMs = 15_000;

/**
 * Maps a room-fetch failure to a terminal closed reason. Returns null for
 * transient errors (network, 5xx) so the reconnect loop keeps retrying.
 */
function closedReasonFromRoomFetchError(error: unknown): string | null {
  if (!(error instanceof ApiClientError)) {
    return null;
  }
  switch (error.status) {
    case 404:
      return "not_found";
    case 410:
      return "ended";
    case 403:
      return "forbidden";
    default:
      return null;
  }
}

function buildRoomWebSocketUrl(
  apiBaseUrl: string,
  roomId: string,
  roomToken: string,
  accessToken: string | null,
  profileId: string | null,
  profileToken: string | null,
) {
  if (typeof window === "undefined") {
    return "";
  }

  const apiBase = apiBaseUrl.startsWith("http")
    ? apiBaseUrl
    : new URL(apiBaseUrl, window.location.origin).toString();
  const wsBase = apiBase.replace(/^http/, "ws");
  const url = new URL(`${wsBase}/watch-together/rooms/${roomId}/ws`);
  url.searchParams.set("room_token", roomToken);
  if (accessToken) {
    url.searchParams.set("token", accessToken);
  }
  if (profileId) {
    url.searchParams.set("profile_id", profileId);
  }
  if (profileToken) {
    url.searchParams.set("profile_token", profileToken);
  }
  return url.toString();
}

export function useWatchTogetherRoomConnection({
  roomId,
  roomToken,
}: UseWatchTogetherRoomConnectionOptions): WatchTogetherRoomConnectionResult {
  const [connectionState, setConnectionState] =
    useState<WatchTogetherConnectionState>("disconnected");
  const [room, setRoom] = useState<WatchTogetherRoomSnapshot | null>(null);
  const [suggestions, setSuggestions] = useState<WatchTogetherSuggestion[]>([]);
  const [closedReason, setClosedReason] = useState<string | null>(null);
  const [transportCommand, setTransportCommand] = useState<WatchTogetherTransportCommand | null>(
    null,
  );
  const [serverTimeOffsetMs, setServerTimeOffsetMs] = useState(0);
  const socketRef = useRef<WebSocket | null>(null);
  const closedReasonRef = useRef<string | null>(null);

  useEffect(() => {
    closedReasonRef.current = closedReason;
  }, [closedReason]);

  // Sets the ref synchronously (before the state commit) so the reconnect
  // loop can never fire between setState and the ref-syncing effect above.
  const markClosed = useCallback((reason: string) => {
    closedReasonRef.current = reason;
    setClosedReason(reason);
  }, []);

  const sendRoomMessage = useCallback((message: Record<string, unknown>) => {
    const socket = socketRef.current;
    if (!socket || socket.readyState !== WebSocket.OPEN) {
      return { ok: false };
    }
    socket.send(JSON.stringify(message));
    return { ok: true };
  }, []);

  useEffect(() => {
    if (!roomId || !roomToken) {
      const resetTimer = window.setTimeout(() => {
        setRoom(null);
        setTransportCommand(null);
        setServerTimeOffsetMs(0);
        setClosedReason(null);
        setConnectionState("disconnected");
      }, 0);
      return () => {
        window.clearTimeout(resetTimer);
      };
    }

    let cancelled = false;
    const resetClosedReasonTimer = window.setTimeout(() => {
      if (!cancelled) {
        setClosedReason(null);
      }
    }, 0);
    void getWatchTogetherRoom(roomId, roomToken)
      .then((response) => {
        if (cancelled) {
          return;
        }
        setRoom(response.room);
      })
      .catch((error: unknown) => {
        if (cancelled) {
          return;
        }
        const reason = closedReasonFromRoomFetchError(error);
        if (reason) {
          markClosed(reason);
        }
      });

    void listWatchTogetherSuggestions(roomId, roomToken)
      .then((response) => {
        if (cancelled) {
          return;
        }
        setSuggestions(response.suggestions);
      })
      .catch(() => {});

    return () => {
      cancelled = true;
      window.clearTimeout(resetClosedReasonTimer);
    };
  }, [markClosed, roomId, roomToken]);

  useEffect(() => {
    if (!roomId || !roomToken) {
      return;
    }

    let disposed = false;
    let attempt = 0;
    let reconnectTimer: number | null = null;
    let pingTimer: number | null = null;

    const sendPing = () => {
      const socket = socketRef.current;
      if (!socket || socket.readyState !== WebSocket.OPEN) {
        return;
      }
      socket.send(
        JSON.stringify({
          type: "ping",
          client_sent_at: new Date().toISOString(),
        }),
      );
    };

    const scheduleReconnect = () => {
      if (disposed || closedReasonRef.current) {
        return;
      }
      const delay = reconnectDelays[Math.min(attempt, reconnectDelays.length - 1)];
      attempt += 1;
      reconnectTimer = window.setTimeout(connect, delay);
    };

    const connect = () => {
      if (disposed) {
        return;
      }

      setConnectionState("connecting");

      let socket: WebSocket;
      try {
        socket = new WebSocket(
          buildRoomWebSocketUrl(
            "/api/v1",
            roomId,
            roomToken,
            getAccessToken(),
            storage.get(storage.KEYS.PROFILE_ID),
            getProfileToken(),
          ),
        );
      } catch {
        scheduleReconnect();
        return;
      }

      socketRef.current = socket;

      socket.addEventListener("open", () => {
        if (disposed) {
          socket.close();
          return;
        }
        attempt = 0;
        setConnectionState("connected");
        sendPing();
        pingTimer = window.setInterval(sendPing, pingIntervalMs);
      });

      socket.addEventListener("message", (event) => {
        let message: Record<string, unknown>;
        try {
          message = JSON.parse(String(event.data)) as Record<string, unknown>;
        } catch {
          return;
        }

        switch (message.type) {
          case "snapshot": {
            const payload = message.room as WatchTogetherRoomSnapshot | undefined;
            if (payload) {
              setRoom(payload);
            }
            return;
          }
          case "suggestions_update": {
            const payload = message.suggestions as WatchTogetherSuggestion[] | undefined;
            if (payload) {
              // Server broadcasts strip voted_by_me (it's relative to the requester).
              // Merge from our local state so each user's votes are preserved.
              setSuggestions((prev) => {
                const prevVotes = new Set(prev.filter((s) => s.voted_by_me).map((s) => s.id));
                return payload.map((s) => ({
                  ...s,
                  voted_by_me: prevVotes.has(s.id),
                }));
              });
            }
            return;
          }
          case "room_closed":
            setRoom(null);
            markClosed(typeof message.reason === "string" ? message.reason : "room_closed");
            socket.close();
            return;
          case "transport_command": {
            const payload = message.command as WatchTogetherTransportCommand | undefined;
            if (payload) {
              setTransportCommand(payload);
            }
            return;
          }
          case "pong":
            if (
              typeof message.client_sent_at === "string" &&
              typeof message.server_received_at === "string" &&
              typeof message.server_sent_at === "string"
            ) {
              const sentAt = Date.parse(message.client_sent_at);
              const serverReceivedAt = Date.parse(message.server_received_at);
              const serverSentAt = Date.parse(message.server_sent_at);
              const receivedAt = Date.now();
              if (
                Number.isFinite(sentAt) &&
                Number.isFinite(serverReceivedAt) &&
                Number.isFinite(serverSentAt)
              ) {
                const offset = (serverReceivedAt - sentAt + (serverSentAt - receivedAt)) / 2;
                setServerTimeOffsetMs(offset);
              }
            }
            return;
          case "error": {
            // Defense in depth: some server paths report terminal conditions
            // as an error message instead of (or before) room_closed.
            if (message.code === "not_found" || message.code === "gone") {
              setRoom(null);
              markClosed(message.code === "gone" ? "ended" : "not_found");
              socket.close();
            } else {
              console.warn("[watch-together]", message.code ?? "error", message.message ?? "");
            }
            return;
          }
          default:
        }
      });

      socket.addEventListener("close", () => {
        if (socketRef.current === socket) {
          socketRef.current = null;
        }
        if (pingTimer !== null) {
          window.clearInterval(pingTimer);
          pingTimer = null;
        }
        setConnectionState("disconnected");
        scheduleReconnect();
      });

      socket.addEventListener("error", () => {
        socket.close();
      });
    };

    connect();

    return () => {
      disposed = true;
      if (reconnectTimer !== null) {
        window.clearTimeout(reconnectTimer);
      }
      if (pingTimer !== null) {
        window.clearInterval(pingTimer);
      }
      const socket = socketRef.current;
      socketRef.current = null;
      if (
        socket &&
        (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)
      ) {
        socket.close();
      }
    };
  }, [markClosed, roomId, roomToken]);

  const updatePolicy = useCallback(
    async (policy: GuestControlPolicy) => {
      if (!roomId) {
        return null;
      }

      const response = await updateWatchTogetherRoomPolicy(roomId, policy);
      setRoom(response.room);
      return response.room;
    },
    [roomId],
  );

  const selectItem = useCallback(
    async (input: SelectWatchTogetherRoomItemInput) => {
      if (!roomId) {
        return null;
      }

      const response = await selectWatchTogetherRoomItem(roomId, input);
      setRoom(response.room);
      return response.room;
    },
    [roomId],
  );

  const closeRoom = useCallback(async () => {
    if (!roomId) {
      return;
    }

    await closeWatchTogetherRoom(roomId);
  }, [roomId]);

  const createSuggestion = useCallback(
    async (input: CreateWatchTogetherSuggestionInput) => {
      if (!roomId || !roomToken) {
        return;
      }

      const response = await createWatchTogetherSuggestion(roomId, roomToken, input);
      setSuggestions(response.suggestions);
    },
    [roomId, roomToken],
  );

  const deleteSuggestion = useCallback(
    async (suggestionId: string) => {
      if (!roomId || !roomToken) {
        return;
      }

      const response = await deleteWatchTogetherSuggestion(roomId, roomToken, suggestionId);
      setSuggestions(response.suggestions);
    },
    [roomId, roomToken],
  );

  const vote = useCallback(
    async (suggestionId: string) => {
      if (!roomId || !roomToken) {
        return;
      }

      const response = await voteWatchTogetherSuggestion(roomId, roomToken, suggestionId);
      setSuggestions(response.suggestions);
    },
    [roomId, roomToken],
  );

  const unvote = useCallback(
    async (suggestionId: string) => {
      if (!roomId || !roomToken) {
        return;
      }

      const response = await unvoteWatchTogetherSuggestion(roomId, roomToken, suggestionId);
      setSuggestions(response.suggestions);
    },
    [roomId, roomToken],
  );

  const promoteSuggestion = useCallback(
    async (suggestionId: string) => {
      if (!roomId || !roomToken) {
        return null;
      }

      const response = await promoteWatchTogetherSuggestion(roomId, roomToken, suggestionId);
      setRoom(response.room);
      return response.room;
    },
    [roomId, roomToken],
  );

  return {
    connectionState,
    room,
    suggestions,
    closedReason,
    transportCommand,
    serverTimeOffsetMs,
    sendRoomMessage,
    updatePolicy,
    selectItem,
    closeRoom,
    createSuggestion,
    deleteSuggestion,
    vote,
    unvote,
    promoteSuggestion,
  };
}
