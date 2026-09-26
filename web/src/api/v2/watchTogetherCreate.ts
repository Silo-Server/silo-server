import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { randomUUID } from "@/lib/uuid";
import type { WatchTogetherSelectionMode } from "@/lib/watchTogether";
import { normalizeRoomResponse } from "./watchTogetherRoomRead";
import { v2 } from "./request";

export function captureRoomCreationDraft(mode: WatchTogetherSelectionMode) {
  return Object.freeze({
    authority: captureProfileRequestContext(),
    body: Object.freeze({ room_id: randomUUID(), selection_mode: mode }),
  });
}
export type RoomCreationDraft = ReturnType<typeof captureRoomCreationDraft>;
export async function createRoom(draft: RoomCreationDraft) {
  const { authority, body } = draft;
  const check = () => {
    if (!authority || !isCapturedProfileAuthorityActive(authority))
      throw new StaleApiRequestContextError();
  };
  check();
  const result = await v2("POST /api/v2/watch-together/rooms", {
    body,
    profileContext: authority!,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    check();
    throw error;
  });
  check();
  return normalizeRoomResponse(body.room_id, result);
}
