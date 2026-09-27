import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { WatchTogetherSuggestion } from "@/lib/watchTogether";
import { v2 } from "./request";

function requireAuthority(
  context: ProfileRequestContextSnapshot | null,
): asserts context is ProfileRequestContextSnapshot {
  if (!context || !isCapturedProfileAuthorityActive(context))
    throw new StaleApiRequestContextError();
}
export async function listRoomSuggestions(
  roomId: string,
  roomToken: string,
  context = captureProfileRequestContext(),
) {
  requireAuthority(context);
  const items: WatchTogetherSuggestion[] = [];
  const ids = new Set<string>(),
    cursors = new Set<string>();
  let cursor: string | undefined;
  for (let page = 0; page < 100; page++) {
    requireAuthority(context);
    const result = await v2("GET /api/v2/watch-together/rooms/{room_id}/suggestions", {
      path: { room_id: roomId },
      headers: { "X-Room-Token": roomToken },
      query: { limit: 100, cursor },
      profileContext: context,
    });
    requireAuthority(context);
    for (const row of result.items) {
      const user = Number(row.suggester_user_id);
      if (
        !Number.isSafeInteger(user) ||
        user <= 0 ||
        ids.has(row.id) ||
        row.room_id !== roomId ||
        !["movie", "episode"].includes(row.content_type)
      )
        throw new Error("Invalid room suggestion.");
      ids.add(row.id);
      items.push({
        ...row,
        suggester_user_id: user,
        content_type: row.content_type as "movie" | "episode",
      });
    }
    if (!result.page) throw new Error("Missing suggestion continuation.");
    if (!result.page.has_more) {
      if (result.page.next_cursor) throw new Error("Invalid suggestion continuation.");
      items.sort(
        (a, b) =>
          b.vote_count - a.vote_count ||
          Date.parse(a.created_at) - Date.parse(b.created_at) ||
          a.id.localeCompare(b.id),
      );
      return { suggestions: items };
    }
    const next = result.page.next_cursor;
    if (!next || cursors.has(next)) throw new Error("Invalid suggestion continuation.");
    cursors.add(next);
    cursor = next;
  }
  throw new Error("Too many room suggestions. Reload the room.");
}
export async function setRoomSuggestionVote(
  roomId: string,
  roomToken: string,
  suggestionId: string,
  vote: boolean,
) {
  const context = captureProfileRequestContext();
  requireAuthority(context);
  const operation = vote
    ? "POST /api/v2/watch-together/rooms/{room_id}/suggestions/{suggestion_id}/vote"
    : "DELETE /api/v2/watch-together/rooms/{room_id}/suggestions/{suggestion_id}/vote";
  await v2(operation, {
    path: { room_id: roomId, suggestion_id: suggestionId },
    headers: { "X-Room-Token": roomToken },
    profileContext: context,
    retryAuthentication: false,
  });
  requireAuthority(context);
  return listRoomSuggestions(roomId, roomToken, context);
}
