import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { AdminPlaybackHistoryItem } from "@/api/types";
import { v2, type V2Query, type V2Result } from "./request";

export type AdminPlaybackHistoryQuery = V2Query<"GET /api/v2/admin/playback-history">;
export type AdminPlaybackHistoryEntry =
  V2Result<"GET /api/v2/admin/playback-history">["items"][number];

/** The v2 page size ceiling; the administrator views ask for at most this. */
export const ADMIN_PLAYBACK_HISTORY_MAX_LIMIT = 200;

// Cache keys contain an opaque generation, never a bearer or PIN token. The
// generation advances whenever the captured account, server, profile or PIN
// authority changes, so a page fetched under one authority is never served
// under another.
let lastAuthority: ProfileRequestContextSnapshot | null = null;
let generation = 0;
export function adminPlaybackHistoryScope() {
  const current = captureProfileRequestContext();
  if (
    current?.authContextVersion !== lastAuthority?.authContextVersion ||
    current?.serverOrigin !== lastAuthority?.serverOrigin ||
    current?.profileId !== lastAuthority?.profileId ||
    current?.profileToken !== lastAuthority?.profileToken
  ) {
    generation++;
    lastAuthority = current;
  }
  return `playback-history:${generation}`;
}

export interface AdminPlaybackHistoryPage {
  items: AdminPlaybackHistoryItem[];
  hasMore: boolean;
  nextCursor: string | null;
}

const positiveInteger = /^[1-9]\d*$/;

function isEntry(value: unknown): value is AdminPlaybackHistoryEntry {
  if (typeof value !== "object" || value === null) return false;
  const entry = value as Record<string, unknown>;
  return (
    typeof entry.session_id === "string" &&
    entry.session_id !== "" &&
    typeof entry.user_id === "string" &&
    positiveInteger.test(entry.user_id) &&
    typeof entry.media_file_id === "string" &&
    positiveInteger.test(entry.media_file_id) &&
    typeof entry.started_at === "string" &&
    typeof entry.ended_at === "string" &&
    typeof entry.watched_seconds === "number" &&
    (entry.duration_seconds === null || typeof entry.duration_seconds === "number") &&
    typeof entry.completed === "boolean"
  );
}

/** Projects a v2 entry onto the row shape the administrator views render. */
export function adminPlaybackHistoryItemFromV2(
  entry: AdminPlaybackHistoryEntry,
): AdminPlaybackHistoryItem {
  return {
    session_id: entry.session_id,
    user_id: Number(entry.user_id),
    username: entry.username,
    profile_id: entry.profile_id,
    profile_name: entry.profile_name,
    media_item_id: entry.media_item_id,
    media_file_id: Number(entry.media_file_id),
    media_title: entry.media_title,
    media_type: entry.media_type,
    play_method: entry.play_method,
    started_at: entry.started_at,
    ended_at: entry.ended_at,
    watched_seconds: entry.watched_seconds,
    duration_seconds: entry.duration_seconds,
    completed: entry.completed,
  };
}

/**
 * Reads one page of the finalized playback log under the authority captured
 * when the query was created. A request whose account, server, profile or PIN
 * authority changed underneath it is refused rather than delivered.
 */
export async function listAdminPlaybackHistory(
  query: AdminPlaybackHistoryQuery,
  scope: string,
  signal?: AbortSignal,
): Promise<AdminPlaybackHistoryPage> {
  const context = captureProfileRequestContext();
  if (!context || adminPlaybackHistoryScope() !== scope) throw new StaleApiRequestContextError();
  const body = await v2("GET /api/v2/admin/playback-history", {
    query,
    profileContext: context,
    signal,
  });
  if (!isCapturedProfileAuthorityActive(context) || adminPlaybackHistoryScope() !== scope)
    throw new StaleApiRequestContextError();
  if (
    !Array.isArray(body.items) ||
    !body.page ||
    typeof body.page.has_more !== "boolean" ||
    (body.page.has_more &&
      (!body.items.length || !body.page.next_cursor || body.page.next_cursor === query.cursor)) ||
    (!body.page.has_more && !!body.page.next_cursor) ||
    !body.items.every(isEntry)
  )
    throw new Error("Invalid playback history page. Reload the page.");
  return {
    items: body.items.map(adminPlaybackHistoryItemFromV2),
    hasMore: body.page.has_more,
    nextCursor: body.page.next_cursor ?? null,
  };
}
