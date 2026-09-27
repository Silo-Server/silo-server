import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";
export async function promoteRoomSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
  authority = captureProfileRequestContext(),
) {
  const check = () => {
    if (!authority || !isCapturedProfileAuthorityActive(authority))
      throw new StaleApiRequestContextError();
  };
  check();
  const result = await v2("POST /api/v2/watch-together/rooms/{room_id}/suggestions/promote", {
    path: { room_id: roomId },
    headers: { "X-Room-Token": roomToken },
    body: { suggestion_id: suggestionId },
    profileContext: authority!,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    check();
    throw error;
  });
  check();
  return normalizeRoomResponse(roomId, result);
}
