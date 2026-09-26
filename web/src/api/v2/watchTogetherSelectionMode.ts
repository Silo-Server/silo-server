import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import type { WatchTogetherSelectionMode } from "@/lib/watchTogether";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";

/** Switches a lobby between host picks and voting. Drops the staged item. */
export async function updateRoomSelectionMode(
  roomId: string,
  mode: WatchTogetherSelectionMode,
  authority = captureProfileRequestContext(),
) {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const result = await v2("PATCH /api/v2/watch-together/rooms/{room_id}/selection-mode", {
    path: { room_id: roomId },
    body: { selection_mode: mode },
    profileContext: authority,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
    throw error;
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  return normalizeRoomResponse(roomId, result);
}
