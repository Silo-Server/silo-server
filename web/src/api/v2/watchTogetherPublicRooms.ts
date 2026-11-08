import type { components } from "./schema";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type {
  WatchTogetherJoinRequest,
  WatchTogetherJoinRequestStatus,
  WatchTogetherPublicRoom,
  WatchTogetherRoomResponse,
  WatchTogetherRoomVisibility,
} from "@/lib/watchTogether";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";

function requireAuthority(
  context: ProfileRequestContextSnapshot | null,
): asserts context is ProfileRequestContextSnapshot {
  if (!context || !isCapturedProfileAuthorityActive(context))
    throw new StaleApiRequestContextError();
}

/** Rethrows a failure as stale when the profile changed while it was in flight. */
function staleAware(context: ProfileRequestContextSnapshot) {
  return (error: unknown): never => {
    if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
    throw error;
  };
}

function numericID(value: string): number {
  const id = Number(value);
  if (!/^[1-9][0-9]*$/.test(value) || !Number.isSafeInteger(id))
    throw new Error("Invalid watch party identity.");
  return id;
}

const requestStatuses: readonly WatchTogetherJoinRequestStatus[] = [
  "pending",
  "approved",
  "denied",
];

/** Validates a join request from the API and converts its ids to numbers. */
export function normalizeJoinRequest(
  row: components["schemas"]["WatchTogetherJoinRequest"],
): WatchTogetherJoinRequest {
  if (!requestStatuses.includes(row.status as WatchTogetherJoinRequestStatus))
    throw new Error("Invalid join request.");
  return {
    ...row,
    user_id: numericID(row.user_id),
    status: row.status as WatchTogetherJoinRequestStatus,
  };
}

/** Lists or unlists a room as its host; returns the authoritative snapshot. */
export async function updateRoomVisibility(
  roomId: string,
  visibility: WatchTogetherRoomVisibility,
  context = captureProfileRequestContext(),
): Promise<WatchTogetherRoomResponse> {
  requireAuthority(context);
  const result = await v2("PATCH /api/v2/watch-together/rooms/{room_id}/visibility", {
    path: { room_id: roomId },
    body: { visibility },
    profileContext: context,
    retryAuthentication: false,
  }).catch(staleAware(context));
  requireAuthority(context);
  return normalizeRoomResponse(roomId, result);
}

/** Reads the open public rooms on this server, newest first. */
export async function listPublicRooms(
  context = captureProfileRequestContext(),
): Promise<WatchTogetherPublicRoom[]> {
  requireAuthority(context);
  const result = await v2("GET /api/v2/watch-together/public-rooms", {
    profileContext: context,
  }).catch(staleAware(context));
  requireAuthority(context);
  return result.rooms.map((room) => {
    if (!["lobby", "playing"].includes(room.phase)) throw new Error("Invalid public room.");
    return {
      ...room,
      phase: room.phase as WatchTogetherPublicRoom["phase"],
      playback_state: room.playback_state as WatchTogetherPublicRoom["playback_state"],
      selection_mode: room.selection_mode as WatchTogetherPublicRoom["selection_mode"],
      host_user_id: numericID(room.host_user_id),
      join_request: room.join_request ? normalizeJoinRequest(room.join_request) : undefined,
    };
  });
}

/** Asks a public room's host to let this profile in. Asking again returns the same request. */
export async function createJoinRequest(
  roomId: string,
  context = captureProfileRequestContext(),
): Promise<WatchTogetherJoinRequest> {
  requireAuthority(context);
  const result = await v2("POST /api/v2/watch-together/rooms/{room_id}/join-requests", {
    path: { room_id: roomId },
    profileContext: context,
    retryAuthentication: false,
  }).catch(staleAware(context));
  requireAuthority(context);
  return normalizeJoinRequest(result.join_request);
}

/**
 * Reads the profile's own request. Once the host approves it, `room` carries
 * the snapshot and room proof, the same response joining by code returns.
 */
export async function readJoinRequest(
  roomId: string,
  requestId: string,
  context = captureProfileRequestContext(),
): Promise<{ request: WatchTogetherJoinRequest; room: WatchTogetherRoomResponse | null }> {
  requireAuthority(context);
  const result = await v2("GET /api/v2/watch-together/rooms/{room_id}/join-requests/{request_id}", {
    path: { room_id: roomId, request_id: requestId },
    profileContext: context,
  }).catch(staleAware(context));
  requireAuthority(context);
  const request = normalizeJoinRequest(result.join_request);
  const room =
    result.room && result.room_access_token
      ? normalizeRoomResponse(roomId, {
          room: result.room,
          room_access_token: result.room_access_token,
        })
      : null;
  return { request, room };
}

/** Reads a room's pending requests as its host, oldest first. */
export async function listJoinRequests(
  roomId: string,
  context = captureProfileRequestContext(),
): Promise<WatchTogetherJoinRequest[]> {
  requireAuthority(context);
  const result = await v2("GET /api/v2/watch-together/rooms/{room_id}/join-requests", {
    path: { room_id: roomId },
    profileContext: context,
  }).catch(staleAware(context));
  requireAuthority(context);
  return result.join_requests.map(normalizeJoinRequest);
}

/** Approves or denies a request as the host. An approval cannot be undone. */
export async function decideJoinRequest(
  roomId: string,
  requestId: string,
  status: Exclude<WatchTogetherJoinRequestStatus, "pending">,
  context = captureProfileRequestContext(),
): Promise<WatchTogetherJoinRequest> {
  requireAuthority(context);
  const result = await v2(
    "PATCH /api/v2/watch-together/rooms/{room_id}/join-requests/{request_id}",
    {
      path: { room_id: roomId, request_id: requestId },
      body: { status },
      profileContext: context,
      retryAuthentication: false,
    },
  ).catch(staleAware(context));
  requireAuthority(context);
  return normalizeJoinRequest(result.join_request);
}
