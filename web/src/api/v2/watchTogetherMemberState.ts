import type { components } from "./schema";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2 } from "./request";

export type MemberWatchStateKind = "unseen" | "in_progress" | "watched";

export interface MemberWatchState {
  user_id: number;
  profile_id: string;
  state: MemberWatchStateKind;
  position_seconds?: number;
  duration_seconds?: number;
  on_watchlist: boolean;
}

export interface ItemMemberState {
  content_id: string;
  members: MemberWatchState[];
}

export interface RoomMemberStateResponse {
  members: components["schemas"]["WatchTogetherRoomMember"][];
  items: ItemMemberState[];
}

/** The server bound on one member-state read. */
export const MAX_MEMBER_STATE_IDS = 200;

function numericID(value: string): number {
  const id = Number(value);
  if (!/^[1-9][0-9]*$/.test(value) || !Number.isSafeInteger(id))
    throw new Error("Invalid member identity.");
  return id;
}

/**
 * Classifies the named content per connected room member. A POST-shaped read;
 * the server changes nothing.
 */
export async function queryRoomMemberState(
  roomId: string,
  roomToken: string,
  contentIds: string[],
  authority = captureProfileRequestContext(),
): Promise<RoomMemberStateResponse> {
  if (!authority || !isCapturedProfileAuthorityActive(authority))
    throw new StaleApiRequestContextError();
  const ids = Array.from(new Set(contentIds.map((id) => id.trim()).filter(Boolean)));
  if (ids.length === 0) return { members: [], items: [] };
  if (ids.length > MAX_MEMBER_STATE_IDS)
    throw new Error(`Member state reads are limited to ${MAX_MEMBER_STATE_IDS} items.`);
  const result = await v2("POST /api/v2/watch-together/rooms/{room_id}/member-state", {
    path: { room_id: roomId },
    headers: { "X-Room-Token": roomToken },
    body: { content_ids: ids },
    profileContext: authority,
    retryAuthentication: false,
  }).catch((error: unknown) => {
    if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
    throw error;
  });
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
  return {
    members: result.members,
    items: result.items.map((item) => ({
      content_id: item.content_id,
      members: item.members.map((member) => ({
        user_id: numericID(member.user_id),
        profile_id: member.profile_id,
        state: member.state,
        position_seconds: member.position_seconds,
        duration_seconds: member.duration_seconds,
        on_watchlist: member.on_watchlist,
      })),
    })),
  };
}
