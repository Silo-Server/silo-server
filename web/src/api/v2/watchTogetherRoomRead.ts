import type { components } from "./schema";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import type { WatchTogetherRoomResponse } from "@/lib/watchTogether";
import { v2 } from "./request";

function numericID(value: string): number {
  const id = Number(value);
  if (typeof value !== "string" || !/^[1-9][0-9]*$/.test(value) || !Number.isSafeInteger(id))
    throw new Error("Invalid room identity.");
  return id;
}
export async function readRoom(
  roomId: string,
  roomToken: string,
  authority = captureProfileRequestContext(),
): Promise<WatchTogetherRoomResponse> {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const result = await v2("GET /api/v2/watch-together/rooms/{room_id}", {
    path: { room_id: roomId },
    headers: { "X-Room-Token": roomToken },
    profileContext: authority,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
    throw error;
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  return normalizeRoomResponse(roomId, result);
}

export function normalizeRoomResponse(
  roomId: string,
  result: components["schemas"]["WatchTogetherRoomReadOutputBody"],
): WatchTogetherRoomResponse {
  const room = result.room;
  if (
    room.room_id !== roomId ||
    typeof result.room_access_token !== "string" ||
    !result.room_access_token ||
    !["lobby", "playing", "ended"].includes(room.phase) ||
    !["idle", "waiting", "paused", "playing"].includes(room.playback_state) ||
    !["host_pick", "vote"].includes(room.selection_mode) ||
    !["host_only", "guest_play_pause"].includes(room.guest_control_policy) ||
    !["host", "guest"].includes(room.self_role) ||
    !Number.isSafeInteger(room.selection_revision) ||
    !Number.isSafeInteger(room.generation) ||
    !Number.isFinite(Date.parse(room.anchor_updated_at)) ||
    (room.selected_content_id !== undefined && typeof room.selected_content_id !== "string")
  )
    throw new Error("Invalid room snapshot.");
  return {
    room_access_token: result.room_access_token,
    room: {
      ...room,
      selected_file_id:
        room.selected_file_id === undefined ? undefined : numericID(room.selected_file_id),
      selected_library_id:
        room.selected_library_id === undefined ? undefined : numericID(room.selected_library_id),
      members: room.members?.map((member) => ({
        ...member,
        user_id: numericID(member.user_id),
        lobby_ready: member.lobby_ready === true,
      })),
    },
  };
}
