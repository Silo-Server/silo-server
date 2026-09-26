import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import type { SelectWatchTogetherRoomItemInput } from "@/lib/watchTogether";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";
function optionalID(value: number | undefined) {
  if (value === undefined) return undefined;
  if (!Number.isSafeInteger(value) || value <= 0)
    throw new Error("Invalid room selection identity.");
  return String(value);
}
export async function selectRoomItem(
  roomId: string,
  input: SelectWatchTogetherRoomItemInput,
  authority = captureProfileRequestContext(),
) {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const result = await v2("PUT /api/v2/watch-together/rooms/{room_id}/selection", {
    path: { room_id: roomId },
    body: {
      content_id: input.content_id,
      file_id: optionalID(input.file_id),
      library_id: optionalID(input.library_id),
    },
    profileContext: authority,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
    throw error;
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  return normalizeRoomResponse(roomId, result);
}
