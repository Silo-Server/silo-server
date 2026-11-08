import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { V2ProblemError } from "@/api/v2/request";
import {
  createJoinRequest,
  listPublicRooms,
  readJoinRequest,
} from "@/api/v2/watchTogetherPublicRooms";
import type {
  WatchTogetherJoinRequest,
  WatchTogetherPublicRoom,
  WatchTogetherRoomResponse,
} from "@/lib/watchTogether";

/** How often the open-party list refreshes while the page is visible. */
const LIST_INTERVAL_MS = 10_000;
/** How often a waiting request is checked for the host's answer. */
const REQUEST_POLL_MS = 3_000;

function pageVisible() {
  return typeof document === "undefined" || document.visibilityState !== "hidden";
}

/**
 * The server's open public rooms and this profile's requests to join them.
 * When the host approves a waiting request, `onApproved` receives the same
 * response joining by code returns, so the caller can enter the room.
 *
 * `profileKey` restarts everything when the signed-in profile changes.
 */
export function usePublicRooms({
  profileKey,
  onApproved,
}: {
  profileKey: string | null;
  onApproved: (response: WatchTogetherRoomResponse) => void;
}) {
  const [rooms, setRooms] = useState<WatchTogetherPublicRoom[] | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [requesting, setRequesting] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [listFailed, setListFailed] = useState(false);
  const onApprovedRef = useRef(onApproved);
  useEffect(() => {
    onApprovedRef.current = onApproved;
  }, [onApproved]);

  const setRequest = useCallback((roomId: string, request: WatchTogetherJoinRequest) => {
    setRooms(
      (current) =>
        current?.map((room) =>
          room.room_id === roomId ? { ...room, join_request: request } : room,
        ) ?? current,
    );
  }, []);

  const refresh = useCallback(async () => {
    const authority = captureProfileRequestContext();
    if (!authority) return;
    try {
      const next = await listPublicRooms(authority);
      if (!isCapturedProfileAuthorityActive(authority)) return;
      setRooms(next);
      setUnavailable(false);
      setListFailed(false);
    } catch (listError) {
      if (listError instanceof StaleApiRequestContextError) return;
      setListFailed(true);
      // A server without public rooms answers 503 or does not know the route.
      if (
        listError instanceof V2ProblemError &&
        (listError.status === 503 || listError.status === 404)
      )
        setUnavailable(true);
    }
  }, []);

  useEffect(() => {
    // A server without public rooms answered once; stop asking.
    if (!profileKey || unavailable) return;
    const first = window.setTimeout(() => void refresh(), 0);
    const timer = window.setInterval(() => {
      if (pageVisible()) void refresh();
    }, LIST_INTERVAL_MS);
    return () => {
      window.clearTimeout(first);
      window.clearInterval(timer);
      setRooms(null);
    };
  }, [profileKey, refresh, unavailable]);

  // Waiting requests, as a stable key so the poll restarts only when the set changes.
  const waiting = useMemo(
    () =>
      (rooms ?? [])
        .filter((room) => room.join_request?.status === "pending")
        .map((room) => [room.room_id, room.join_request!.id] as const),
    [rooms],
  );
  const waitingKey = JSON.stringify(waiting);

  const checkRequest = useCallback(
    async (roomId: string, requestId: string) => {
      const authority = captureProfileRequestContext();
      if (!authority) return;
      try {
        const { request, room } = await readJoinRequest(roomId, requestId, authority);
        if (!isCapturedProfileAuthorityActive(authority)) return;
        setRequest(roomId, request);
        if (room) onApprovedRef.current(room);
      } catch (readError) {
        if (readError instanceof StaleApiRequestContextError) return;
        // The room ended or the request is gone: the next list read drops it.
        if (
          readError instanceof V2ProblemError &&
          (readError.status === 404 || readError.status === 409)
        )
          void refresh();
      }
    },
    [refresh, setRequest],
  );

  useEffect(() => {
    const pending = JSON.parse(waitingKey) as [string, string][];
    if (pending.length === 0) return;
    const timer = window.setInterval(() => {
      if (!pageVisible()) return;
      for (const [roomId, requestId] of pending) void checkRequest(roomId, requestId);
    }, REQUEST_POLL_MS);
    return () => window.clearInterval(timer);
  }, [checkRequest, waitingKey]);

  const requestToJoin = useCallback(
    async (roomId: string) => {
      const authority = captureProfileRequestContext();
      if (!authority) return;
      setRequesting(roomId);
      setError(null);
      try {
        const request = await createJoinRequest(roomId, authority);
        if (!isCapturedProfileAuthorityActive(authority)) return;
        setRequest(roomId, request);
        // Already let in (asked earlier, or approved from another device).
        if (request.status === "approved") await checkRequest(roomId, request.id);
      } catch (createError) {
        if (createError instanceof StaleApiRequestContextError) return;
        if (createError instanceof V2ProblemError && createError.status === 409) {
          setError("That party is no longer open.");
          void refresh();
          return;
        }
        setError(createError instanceof Error ? createError.message : "Could not ask to join.");
      } finally {
        setRequesting((current) => (current === roomId ? null : current));
      }
    },
    [checkRequest, refresh, setRequest],
  );

  return { rooms, unavailable, listFailed, requesting, error, requestToJoin, checkRequest };
}
