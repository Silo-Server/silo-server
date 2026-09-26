import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";

/** Starts whatever the lobby has staged. Non-retryable: never replay an uncertain result. */
export async function startRoomPlayback(
  roomId: string,
  authority = captureProfileRequestContext(),
) {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const result = await v2("POST /api/v2/watch-together/rooms/{room_id}/playback/start", {
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
