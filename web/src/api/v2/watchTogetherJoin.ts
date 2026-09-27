import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import type { JoinWatchTogetherRoomInput } from "@/lib/watchTogether";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";
export async function joinRoom(
  input: JoinWatchTogetherRoomInput,
  authority = captureProfileRequestContext(),
) {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const result = await v2("POST /api/v2/watch-together/join", {
    body: { ...input },
    profileContext: authority,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
    throw error;
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  if (typeof result.room?.room_id !== "string" || !result.room.room_id)
    throw new Error("Invalid joined room identity.");
  return normalizeRoomResponse(result.room.room_id, result);
}
