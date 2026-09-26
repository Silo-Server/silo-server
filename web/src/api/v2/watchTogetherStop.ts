import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";

/**
 * Stops playback for everyone and returns the room to the lobby with the
 * same item staged. Naturally idempotent: a room that is not playing answers
 * with its current snapshot.
 */
export async function stopRoomPlayback(roomId: string, authority = captureProfileRequestContext()) {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const result = await v2("POST /api/v2/watch-together/rooms/{room_id}/playback/stop", {
    path: { room_id: roomId },
    profileContext: authority,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
    throw error;
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  return normalizeRoomResponse(roomId, result);
}
