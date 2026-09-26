import {
  fallbackRoomSource,
  type SourceFallbackRequest,
} from "@/api/v2/watchTogetherSourceFallback";
import { useOptionalAuth } from "@/hooks/useAuth";
import { mintRoomSocketTicket, roomSocketURL } from "@/api/v2/watchTogetherSocket";
import type { SuggestionCreationDraft } from "@/api/v2/watchTogetherSuggestionCreate";
import { V2ProblemError } from "@/api/v2/request";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  ApiClientError,
  StaleApiRequestContextError,
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
} from "@/api/client";
import {
  closeWatchTogetherRoom,
  createWatchTogetherSuggestion,
  deleteWatchTogetherSuggestion,
  type GuestControlPolicy,
  getWatchTogetherRoom,
  listWatchTogetherSuggestions,
  promoteWatchTogetherSuggestion,
  selectWatchTogetherRoomItem,
  stageWatchTogetherRoomItem,
  startWatchTogetherRoomPlayback,
  stopWatchTogetherRoomPlayback,
  updateWatchTogetherRoomSelectionMode,
  type SelectWatchTogetherRoomItemInput,
  type WatchTogetherSelectionMode,
  unvoteWatchTogetherSuggestion,
  updateWatchTogetherRoomPolicy,
  voteWatchTogetherSuggestion,
  type WatchTogetherTransportCommand,
  type WatchTogetherRoomSnapshot,
  type WatchTogetherSuggestion,
} from "@/lib/watchTogether";

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
  replacementReason: string | null;
  rejoinRoom: () => void;
  transportCommand: WatchTogetherTransportCommand | null;
  serverTimeOffsetMs: number;
  sendRoomMessage: (message: Record<string, unknown>) => SendRoomMessageResult;
  updatePolicy: (policy: GuestControlPolicy) => Promise<WatchTogetherRoomSnapshot | null>;
  selectItem: (
    input: SelectWatchTogetherRoomItemInput,
  ) => Promise<WatchTogetherRoomSnapshot | null>;
  fallbackSource: (input: SourceFallbackRequest) => Promise<WatchTogetherRoomSnapshot | null>;
  /** Stages content in a host-pick lobby; nothing plays until startPlayback. */
  stageItem: (input: SelectWatchTogetherRoomItemInput) => Promise<WatchTogetherRoomSnapshot | null>;
  /** Starts the staged content for everyone. */
  startPlayback: () => Promise<WatchTogetherRoomSnapshot | null>;
  /** Stops playback for everyone; the room returns to the lobby, still staged. */
  stopPlayback: () => Promise<WatchTogetherRoomSnapshot | null>;
  /** Switches a lobby between host picks and voting. */
  updateSelectionMode: (
    mode: WatchTogetherSelectionMode,
  ) => Promise<WatchTogetherRoomSnapshot | null>;
  /** Marks this member ready (or not) in the lobby. Sent over the socket and shared across API servers. */
  setLobbyReady: (ready: boolean) => SendRoomMessageResult;
  closeRoom: () => Promise<void>;
  createSuggestion: (draft: SuggestionCreationDraft) => Promise<void>;
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
  if (!(error instanceof ApiClientError) && !(error instanceof V2ProblemError)) {
    return null;
  }
  switch (error.status) {
    case 404:
      return "not_found";
    case 410:
      return "ended";
    case 409:
      return error instanceof V2ProblemError && error.operationId === "getWatchTogetherRoom"
        ? "ended"
        : null;
    case 403:
      return "forbidden";
    default:
      return null;
  }
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
  const [replacement, setReplacement] = useState<{ scope: string; reason: string } | null>(null);
  const [rejoinAttempt, setRejoinAttempt] = useState(0);
  const [transportCommand, setTransportCommand] = useState<WatchTogetherTransportCommand | null>(
    null,
  );
  const [serverTimeOffsetMs, setServerTimeOffsetMs] = useState(0);
  const socketRef = useRef<WebSocket | null>(null);
  // Subscribe to the production AuthProvider so PIN/profile replacement can
  // rebind the socket even when room props remain unchanged.
  useOptionalAuth();
  const renderedAuthority = captureProfileRequestContext();
  // Credential rotation must not let a displaced connection reclaim this profile.
  const replacementScope = JSON.stringify([
    roomId,
    renderedAuthority?.authContextVersion,
    renderedAuthority?.serverOrigin,
    renderedAuthority?.profileId,
  ]);
  const replacementScopeRef = useRef<string | null>(null);
  const replacementReason = replacement?.scope === replacementScope ? replacement.reason : null;
  const rejoinRoom = useCallback(() => {
    if (replacementScopeRef.current !== replacementScope) return;
    replacementScopeRef.current = null;
    setReplacement(null);
    // A selection may have changed on the winner while this device was away.
    // Let the fresh room read or socket snapshot choose what playback to enter.
    setRoom(null);
    setRejoinAttempt((attempt) => attempt + 1);
  }, [replacementScope]);
  const socketAuthorityRef = useRef<ReturnType<typeof captureProfileRequestContext>>(null);
  const closedReasonRef = useRef<string | null>(null);
  const socketSnapshotVersion = useRef(0);
  const applyHTTPRoomSnapshot = useCallback(
    (incoming: WatchTogetherRoomSnapshot, socketVersionAtRequest: number) => {
      if (closedReasonRef.current) return;
      const receivedSocketSnapshot = socketSnapshotVersion.current !== socketVersionAtRequest;
      // Runtime-only changes do not advance generation. Preserve the socket's
      // attachment/readiness when an overlapping HTTP request returns that generation.
      setRoom((current) =>
        current?.room_id === incoming.room_id &&
        (current.generation > incoming.generation ||
          (receivedSocketSnapshot && current.generation === incoming.generation))
          ? current
          : incoming,
      );
    },
    [],
  );

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
    if (
      !socket ||
      socket.readyState !== WebSocket.OPEN ||
      !socketAuthorityRef.current ||
      !isCapturedProfileAuthorityActive(socketAuthorityRef.current)
    ) {
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
      if (!cancelled && !closedReasonRef.current) {
        setClosedReason(null);
      }
    }, 0);
    const readAuthority = captureProfileRequestContext();
    const socketVersionAtRequest = socketSnapshotVersion.current;
    void getWatchTogetherRoom(roomId, roomToken, readAuthority)
      .then((response) => {
        if (cancelled || !readAuthority || !isCapturedProfileAuthorityActive(readAuthority)) {
          return;
        }
        applyHTTPRoomSnapshot(response.room, socketVersionAtRequest);
      })
      .catch((error: unknown) => {
        if (cancelled || !readAuthority || !isCapturedProfileAuthorityActive(readAuthority)) {
          return;
        }
        const reason = closedReasonFromRoomFetchError(error);
        if (reason) {
          window.clearTimeout(resetClosedReasonTimer);
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
  }, [
    applyHTTPRoomSnapshot,
    markClosed,
    roomId,
    roomToken,
    rejoinAttempt,
    renderedAuthority?.authContextVersion,
    renderedAuthority?.serverOrigin,
    renderedAuthority?.profileId,
    renderedAuthority?.profileToken,
  ]);

  useEffect(() => {
    if (!roomId || !roomToken) {
      return;
    }

    const authority = captureProfileRequestContext();
    if (!authority) return;
    if (replacementScopeRef.current === replacementScope) return;
    closedReasonRef.current = null;
    let disposed = false;
    const active = () =>
      !disposed &&
      replacementScopeRef.current !== replacementScope &&
      isCapturedProfileAuthorityActive(authority);
    let attempt = 0;
    let reconnectTimer: number | null = null;
    let pingTimer: number | null = null;

    const sendPing = () => {
      const socket = socketRef.current;
      if (!active() || !socket || socket.readyState !== WebSocket.OPEN) {
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
      if (!active() || closedReasonRef.current) {
        return;
      }
      const delay = reconnectDelays[Math.min(attempt, reconnectDelays.length - 1)];
      attempt += 1;
      reconnectTimer = window.setTimeout(() => void connect(), delay);
    };

    const connect = async () => {
      if (!active()) {
        return;
      }

      setConnectionState("connecting");

      let socket: WebSocket;
      try {
        const ticket = await mintRoomSocketTicket(roomId, roomToken, authority);
        if (!active() || closedReasonRef.current) return;
        socket = new WebSocket(roomSocketURL(roomId), [
          ticket.protocol,
          `silo.ticket.${ticket.ticket}`,
        ]);
      } catch (error) {
        if (!active()) return;
        if (error instanceof V2ProblemError && [401, 403, 404, 409, 422].includes(error.status)) {
          markClosed(
            error.status === 409 ? "ended" : error.status === 404 ? "not_found" : "forbidden",
          );
          return;
        }
        scheduleReconnect();
        return;
      }
      socketAuthorityRef.current = authority;

      socketRef.current = socket;

      socket.addEventListener("open", () => {
        if (!active() || socketRef.current !== socket || socket.protocol !== "silo.room.v2") {
          socket.close();
          return;
        }
        attempt = 0;
        setConnectionState("connected");
        sendPing();
        pingTimer = window.setInterval(sendPing, pingIntervalMs);
      });

      socket.addEventListener("message", (event) => {
        if (!active() || socketRef.current !== socket) return;
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
              socketSnapshotVersion.current++;
              setRoom((current) =>
                current?.room_id === payload.room_id && current.generation > payload.generation
                  ? current
                  : payload,
              );
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
          case "connection_replaced":
            replacementScopeRef.current = replacementScope;
            setReplacement({
              scope: replacementScope,
              reason:
                typeof message.reason === "string" && message.reason
                  ? message.reason
                  : "This profile joined the Watch Party on another device.",
            });
            if (reconnectTimer !== null) window.clearTimeout(reconnectTimer);
            if (pingTimer !== null) window.clearInterval(pingTimer);
            reconnectTimer = null;
            pingTimer = null;
            socketRef.current = null;
            setTransportCommand(null);
            setConnectionState("disconnected");
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
        if (!active() || socketRef.current !== socket) return;
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
        if (!active() || socketRef.current !== socket) return;
        socket.close();
      });
    };

    void connect();

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
  }, [
    markClosed,
    roomId,
    roomToken,
    rejoinAttempt,
    replacementScope,
    renderedAuthority?.authContextVersion,
    renderedAuthority?.serverOrigin,
    renderedAuthority?.profileId,
    renderedAuthority?.profileToken,
  ]);

  const policyAuthority = captureProfileRequestContext();
  const policyRun = useRef(0);
  const policyRoom = useRef(roomId);
  const invalidatePolicy = useCallback(() => {
    policyRun.current++;
  }, []);
  useLayoutEffect(() => {
    policyRoom.current = roomId;
    invalidatePolicy();
    return invalidatePolicy;
  }, [roomId, invalidatePolicy]);
  const updatePolicy = useCallback(
    async (policy: GuestControlPolicy) => {
      if (!roomId) return null;
      const run = ++policyRun.current;
      const socketVersionAtRequest = socketSnapshotVersion.current;
      const response = await updateWatchTogetherRoomPolicy(roomId, policy, policyAuthority).catch(
        (error: unknown) => {
          if (policyRoom.current !== roomId || policyRun.current !== run) return null;
          throw error;
        },
      );
      if (!response) return null;
      if (policyRoom.current !== roomId || policyRun.current !== run) return null;
      if (!policyAuthority || !isCapturedProfileAuthorityActive(policyAuthority)) return null;
      applyHTTPRoomSnapshot(response.room, socketVersionAtRequest);
      return response.room;
    },
    [roomId, policyAuthority, applyHTTPRoomSnapshot],
  );

  const selectionAuthority = captureProfileRequestContext();
  const selectionRun = useRef(0);
  const selectionRoom = useRef(roomId);
  const invalidateSelection = useCallback(() => {
    selectionRun.current++;
  }, []);
  useLayoutEffect(() => {
    selectionRoom.current = roomId;
    invalidateSelection();
    return invalidateSelection;
  }, [roomId, invalidateSelection]);
  const selectItem = useCallback(
    async (input: SelectWatchTogetherRoomItemInput) => {
      if (!roomId) return null;
      const run = ++selectionRun.current;
      const socketVersionAtRequest = socketSnapshotVersion.current;
      const response = await selectWatchTogetherRoomItem(
        roomId,
        { ...input },
        selectionAuthority,
      ).catch((error: unknown) => {
        if (selectionRoom.current !== roomId || selectionRun.current !== run) return null;
        throw error;
      });
      if (!response) return null;
      if (selectionRoom.current !== roomId || selectionRun.current !== run) return null;
      if (!selectionAuthority || !isCapturedProfileAuthorityActive(selectionAuthority)) return null;
      applyHTTPRoomSnapshot(response.room, socketVersionAtRequest);
      return response.room;
    },
    [roomId, selectionAuthority, applyHTTPRoomSnapshot],
  );

  // Stage, start and mode switch share the selection fencing: one run counter
  // per action, invalidated when the room changes, and a receipt is published
  // only under the authority it was captured with.
  const lobbyAuthority = captureProfileRequestContext();
  const lobbyRun = useRef(0);
  const lobbyRoom = useRef(roomId);
  const invalidateLobby = useCallback(() => {
    lobbyRun.current++;
  }, []);
  useLayoutEffect(() => {
    lobbyRoom.current = roomId;
    invalidateLobby();
    return invalidateLobby;
  }, [roomId, invalidateLobby]);
  const publishLobbyReceipt = useCallback(
    (run: number, response: { room: WatchTogetherRoomSnapshot } | null) => {
      if (!response) return null;
      if (lobbyRoom.current !== roomId || lobbyRun.current !== run) return null;
      if (!lobbyAuthority || !isCapturedProfileAuthorityActive(lobbyAuthority)) return null;
      setRoom((current) =>
        current &&
        current.room_id === response.room.room_id &&
        current.generation > response.room.generation
          ? current
          : response.room,
      );
      return response.room;
    },
    [roomId, lobbyAuthority],
  );
  const stageItem = useCallback(
    async (input: SelectWatchTogetherRoomItemInput) => {
      if (!roomId) return null;
      const run = ++lobbyRun.current;
      const response = await stageWatchTogetherRoomItem(roomId, { ...input }, lobbyAuthority).catch(
        (error: unknown) => {
          if (lobbyRoom.current !== roomId || lobbyRun.current !== run) return null;
          throw error;
        },
      );
      return publishLobbyReceipt(run, response);
    },
    [roomId, lobbyAuthority, publishLobbyReceipt],
  );
  const startPlayback = useCallback(async () => {
    if (!roomId) return null;
    const run = ++lobbyRun.current;
    const response = await startWatchTogetherRoomPlayback(roomId, lobbyAuthority).catch(
      (error: unknown) => {
        if (lobbyRoom.current !== roomId || lobbyRun.current !== run) return null;
        throw error;
      },
    );
    return publishLobbyReceipt(run, response);
  }, [roomId, lobbyAuthority, publishLobbyReceipt]);
  const stopPlayback = useCallback(async () => {
    if (!roomId) return null;
    const run = ++lobbyRun.current;
    const response = await stopWatchTogetherRoomPlayback(roomId, lobbyAuthority).catch(
      (error: unknown) => {
        if (lobbyRoom.current !== roomId || lobbyRun.current !== run) return null;
        throw error;
      },
    );
    return publishLobbyReceipt(run, response);
  }, [roomId, lobbyAuthority, publishLobbyReceipt]);
  const updateSelectionMode = useCallback(
    async (mode: WatchTogetherSelectionMode) => {
      if (!roomId) return null;
      const run = ++lobbyRun.current;
      const response = await updateWatchTogetherRoomSelectionMode(
        roomId,
        mode,
        lobbyAuthority,
      ).catch((error: unknown) => {
        if (lobbyRoom.current !== roomId || lobbyRun.current !== run) return null;
        throw error;
      });
      return publishLobbyReceipt(run, response);
    },
    [roomId, lobbyAuthority, publishLobbyReceipt],
  );
  const setLobbyReady = useCallback(
    (ready: boolean) => sendRoomMessage({ type: "lobby_ready", ready }),
    [sendRoomMessage],
  );

  const closeAuthority = captureProfileRequestContext();
  const closeRoom = useCallback(async () => {
    if (!roomId) {
      return;
    }

    await closeWatchTogetherRoom(roomId, closeAuthority);
  }, [roomId, closeAuthority]);

  const creationRun = useRef(0);
  const creationRoom = useRef(roomId);
  const invalidateCreation = useCallback(() => {
    creationRun.current++;
  }, []);
  useLayoutEffect(() => {
    creationRoom.current = roomId;
    invalidateCreation();
    return invalidateCreation;
  }, [roomId, roomToken, invalidateCreation]);
  const createSuggestion = useCallback(
    async (draft: SuggestionCreationDraft) => {
      if (!roomId || draft.roomId !== roomId || creationRoom.current !== roomId)
        throw new StaleApiRequestContextError();
      const run = ++creationRun.current;
      const response = await createWatchTogetherSuggestion(draft).catch((error: unknown) => {
        if (run !== creationRun.current) return null;
        throw error;
      });
      if (!response || run !== creationRun.current) throw new StaleApiRequestContextError();
      if (!draft.authority || !isCapturedProfileAuthorityActive(draft.authority))
        throw new StaleApiRequestContextError();
      setSuggestions(response.suggestions);
    },
    [roomId],
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

  const promotionAuthority = captureProfileRequestContext();
  const promotionRun = useRef(0);
  const promotionRoom = useRef(roomId);
  const invalidatePromotion = useCallback(() => {
    promotionRun.current++;
  }, []);
  useLayoutEffect(() => {
    promotionRoom.current = roomId;
    invalidatePromotion();
    return invalidatePromotion;
  }, [roomId, roomToken, invalidatePromotion]);
  const promoteSuggestion = useCallback(
    async (suggestionId: string) => {
      if (!roomId || !roomToken || promotionRoom.current !== roomId) return null;
      const run = ++promotionRun.current;
      const socketVersionAtRequest = socketSnapshotVersion.current;
      const response = await promoteWatchTogetherSuggestion(
        roomId,
        roomToken,
        suggestionId,
        promotionAuthority,
      ).catch((error: unknown) => {
        if (run !== promotionRun.current) return null;
        throw error;
      });
      if (!response || run !== promotionRun.current) return null;
      if (!promotionAuthority || !isCapturedProfileAuthorityActive(promotionAuthority)) return null;
      applyHTTPRoomSnapshot(response.room, socketVersionAtRequest);
      return response.room;
    },
    [roomId, roomToken, promotionAuthority, applyHTTPRoomSnapshot],
  );

  const fallbackAuthority = captureProfileRequestContext();
  const fallbackContextRef = useRef({ roomId, roomToken });
  useLayoutEffect(() => {
    fallbackContextRef.current = { roomId, roomToken };
    return () => {
      fallbackContextRef.current = { roomId: null, roomToken: null };
    };
  }, [roomId, roomToken]);
  const fallbackSource = useCallback(
    async (input: SourceFallbackRequest) => {
      const context = fallbackContextRef.current;
      if (!roomId || !roomToken || context.roomId !== roomId || context.roomToken !== roomToken)
        return null;
      const socketVersionAtRequest = socketSnapshotVersion.current;
      const response = await fallbackRoomSource(roomId, roomToken, input, fallbackAuthority);
      if (fallbackContextRef.current !== context) return null;
      applyHTTPRoomSnapshot(response.room, socketVersionAtRequest);
      return response.room;
    },
    [roomId, roomToken, fallbackAuthority, applyHTTPRoomSnapshot],
  );

  return {
    connectionState,
    room,
    suggestions,
    closedReason,
    replacementReason,
    rejoinRoom,
    transportCommand,
    serverTimeOffsetMs,
    sendRoomMessage,
    updatePolicy,
    selectItem,
    fallbackSource,
    stageItem,
    startPlayback,
    stopPlayback,
    updateSelectionMode,
    setLobbyReady,
    closeRoom,
    createSuggestion,
    deleteSuggestion,
    vote,
    unvote,
    promoteSuggestion,
  };
}
