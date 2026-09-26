import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "./request";
import { listRoomSuggestions } from "./watchTogetherSuggestions";

export async function deleteRoomSuggestion(
  roomId: string,
  roomToken: string,
  suggestionId: string,
) {
  const context = captureProfileRequestContext();
  if (!context || !isCapturedProfileAuthorityActive(context))
    throw new StaleApiRequestContextError();
  await v2("DELETE /api/v2/watch-together/rooms/{room_id}/suggestions/{suggestion_id}", {
    path: { room_id: roomId, suggestion_id: suggestionId },
    headers: { "X-Room-Token": roomToken },
    profileContext: context,
    retryAuthentication: false,
  });
  if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
  return listRoomSuggestions(roomId, roomToken, context);
}
