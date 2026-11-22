import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { AdminPlaybackHistoryItem } from "@/api/types";
import { v2, V2ProblemError, type V2Query, type V2Result } from "./request";
import { t } from "@/i18n";

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
    typeof entry.end_position_seconds === "number" &&
    (entry.duration_seconds === null || typeof entry.duration_seconds === "number") &&
    typeof entry.completed === "boolean" &&
    (entry.played_seconds === null || typeof entry.played_seconds === "number")
  );
}

function parseOptionalNumericId(value: string | null | undefined): number | null {
  if (!value) return null;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null;
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
    end_position_seconds: entry.end_position_seconds,
    duration_seconds: entry.duration_seconds,
    completed: entry.completed,
    played_seconds: entry.played_seconds,
    measured: entry.measured,
    start_position_seconds: entry.start_position_seconds,
    paused_seconds: entry.paused_seconds,
    measured_since: entry.measured_since,
    measured_until: entry.measured_until,
    client_ip: entry.client_ip,
    device_id: entry.device_id,
    device_platform: entry.device_platform,
    client_name: entry.client_name,
    client_version: entry.client_version,
    video_decision: entry.video_decision,
    audio_decision: entry.audio_decision,
    end_reason: entry.end_reason,
    series_title: entry.series_title,
    season_number: entry.season_number,
    episode_number: entry.episode_number,
    subtitle: entry.subtitle,
    target_video_codec: entry.target_video_codec,
    target_resolution: entry.target_resolution,
    target_audio_codec: entry.target_audio_codec,
    target_audio_channels: entry.target_audio_channels,
    output_container: entry.output_container,
    output_protocol: entry.output_protocol,
    target_bitrate_kbps: entry.target_bitrate_kbps,
    transcode_hw_accel: entry.transcode_hw_accel,
    tone_map_mode: entry.tone_map_mode,
    adaptation_reasons: entry.adaptation_reasons ?? [],
    reporting_node: entry.reporting_node,
    stream_location: entry.stream_location,
    compat_origin: entry.compat_origin,
    is_jellyfin_client: entry.is_jellyfin_client,
    routing_execution: entry.routing_execution,
    routing_execution_node_id: parseOptionalNumericId(entry.routing_execution_node_id),
    routing_execution_node_name: entry.routing_execution_node_name,
    routing_egress: entry.routing_egress,
    routing_egress_node_id: parseOptionalNumericId(entry.routing_egress_node_id),
    routing_egress_node_name: entry.routing_egress_node_name,
    route_facts_recorded: entry.route_facts_recorded,
    source_video_codec: entry.source_video_codec,
    source_resolution: entry.source_resolution,
    source_audio_codec: entry.source_audio_codec,
    source_audio_channels: entry.source_audio_channels,
    source_container: entry.source_container,
    source_bitrate_kbps: entry.source_bitrate_kbps,
    source_hdr: entry.source_hdr,
    source_video_range_type: entry.source_video_range_type,
    library_id: parseOptionalNumericId(entry.library_id),
    library_name: entry.library_name,
  };
}

/** Reads one finalized playback session by ID; returns null on 404. */
export async function getAdminPlaybackHistoryEntry(
  sessionId: string,
  scope: string,
  signal?: AbortSignal,
): Promise<AdminPlaybackHistoryItem | null> {
  const context = captureProfileRequestContext();
  if (!context || adminPlaybackHistoryScope() !== scope) throw new StaleApiRequestContextError();
  try {
    const entry = await v2("GET /api/v2/admin/playback-history/{session_id}", {
      path: { session_id: sessionId },
      profileContext: context,
      signal,
    });
    if (!isCapturedProfileAuthorityActive(context) || adminPlaybackHistoryScope() !== scope) {
      throw new StaleApiRequestContextError();
    }
    if (!isEntry(entry)) {
      throw new Error(t("web.admin.errors.playbackHistoryInvalid"));
    }
    return adminPlaybackHistoryItemFromV2(entry);
  } catch (err) {
    if (err instanceof V2ProblemError && err.status === 404) {
      return null;
    }
    throw err;
  }
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
    throw new Error(t("web.admin.errors.playbackHistoryInvalid"));
  return {
    items: body.items.map(adminPlaybackHistoryItemFromV2),
    hasMore: body.page.has_more,
    nextCursor: body.page.next_cursor ?? null,
  };
}
