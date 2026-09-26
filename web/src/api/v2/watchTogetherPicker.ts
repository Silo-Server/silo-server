import type { components } from "./schema";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "./request";

export type PickerItem = components["schemas"]["CatalogItem"];

export interface PickerMember {
  user_id: number;
  profile_id: string;
  display_name: string;
  position_seconds?: number;
  duration_seconds?: number;
}

export interface PickerNextUp {
  content_id: string;
  season_number: number;
  episode_number: number;
  title?: string;
  member_count: number;
}

export interface PickerEntry {
  item: PickerItem;
  members: PickerMember[];
  next_up?: PickerNextUp;
}

export interface RoomPickerResponse {
  members: components["schemas"]["WatchTogetherRoomMember"][];
  continue_together: PickerEntry[];
  watchlist_union: PickerEntry[];
}

function numericID(value: string): number {
  const id = Number(value);
  if (!/^[1-9][0-9]*$/.test(value) || !Number.isSafeInteger(id))
    throw new Error("Invalid member identity.");
  return id;
}

function entryOf(entry: components["schemas"]["WatchTogetherPickerEntry"]): PickerEntry {
  return {
    item: entry.item,
    members: entry.members.map((member) => ({
      user_id: numericID(member.user_id),
      profile_id: member.profile_id,
      display_name: member.display_name,
      position_seconds: member.position_seconds,
      duration_seconds: member.duration_seconds,
    })),
    next_up: entry.next_up,
  };
}

/** The rows the room picker leads with, computed server-side over connected members. */
export async function readRoomPicker(
  roomId: string,
  roomToken: string,
  authority = captureProfileRequestContext(),
): Promise<RoomPickerResponse> {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const result = await v2("GET /api/v2/watch-together/rooms/{room_id}/picker", {
    path: { room_id: roomId },
    headers: { "X-Room-Token": roomToken },
    profileContext: authority,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
    throw error;
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  return {
    members: result.members,
    continue_together: result.continue_together.map(entryOf),
    watchlist_union: result.watchlist_union.map(entryOf),
  };
}
