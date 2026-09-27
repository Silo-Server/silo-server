import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import type { GuestControlPolicy } from "@/lib/watchTogether";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";
export async function updateRoomPolicy(
  roomId: string,
  policy: GuestControlPolicy,
  authority = captureProfileRequestContext(),
) {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const result = await v2("PATCH /api/v2/watch-together/rooms/{room_id}/policy", {
    path: { room_id: roomId },
    body: { guest_control_policy: policy },
    profileContext: authority,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
    throw error;
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  return normalizeRoomResponse(roomId, result);
}
