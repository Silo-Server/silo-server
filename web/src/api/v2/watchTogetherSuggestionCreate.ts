import { randomUUID } from "@/lib/uuid";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import type { CreateWatchTogetherSuggestionInput } from "@/lib/watchTogether";
import { listRoomSuggestions } from "./watchTogetherSuggestions";
import { v2 } from "./request";
export function captureSuggestionDraft(
  roomId: string,
  roomToken: string,
  input: CreateWatchTogetherSuggestionInput,
) {
  return Object.freeze({
    roomId,
    roomToken,
    authority: captureProfileRequestContext(),
    body: Object.freeze({ suggestion_id: randomUUID(), ...input }),
  });
}
export type SuggestionCreationDraft = ReturnType<typeof captureSuggestionDraft>;
export async function createRoomSuggestion(draft: SuggestionCreationDraft) {
  const { authority, roomId, roomToken, body } = draft;
  const check = () => {
    if (!authority || !isCapturedProfileAuthorityActive(authority))
      throw new StaleApiRequestContextError();
  };
  check();
  const result = await v2("POST /api/v2/watch-together/rooms/{room_id}/suggestions", {
    path: { room_id: roomId },
    headers: { "X-Room-Token": roomToken },
    body,
    profileContext: authority!,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    check();
    throw error;
  });
  check();
  if (result.suggestion_id !== body.suggestion_id) throw new Error("Invalid suggestion receipt.");
  return listRoomSuggestions(roomId, roomToken, authority).catch((error: unknown) => {
    check();
    throw error;
  });
}
