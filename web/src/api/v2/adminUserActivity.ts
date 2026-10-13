/**
 * Read-only projections of one account's activity for the admin user page:
 * registered devices, per-profile last use, watch totals, request quota use,
 * live sessions, the finalized watch history, and the account's managed
 * downloads and series monitors.
 *
 * Every read runs under the admin authority captured by the caller and is
 * refused before and after the request when that authority changed, so a
 * page fetched for one admin/profile is never delivered to another. Pages are
 * validated the same way `listAdminUsers` validates them: `has_more` must
 * agree with `next_cursor`, and a cursor never repeats.
 */
import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AdminSession } from "@/api/types";
import { requireAdminUserAuthority } from "./adminUsers";
import { v2 } from "./request";

export interface AdminUserDeviceProfileRow {
  profile_id: string;
  profile_name: string;
  override_count: number;
  last_seen_at: string | null;
}
export interface AdminUserDeviceRow {
  device_id: string;
  device_name: string;
  device_platform: string;
  last_seen_at: string | null;
  last_updated: string | null;
  override_count: number;
  profiles: AdminUserDeviceProfileRow[];
}
export interface AdminUserProfileActivity {
  id: string;
  name: string;
  last_seen_at: string | null;
}
export interface AdminUserWatchSummary {
  days: number;
  since: string;
  plays: number;
  completed_plays: number;
  watched_seconds: number;
  last_played_at: string | null;
}
export interface AdminRequestUsage {
  requests_enabled: boolean;
  allowed: boolean;
  unlimited: boolean;
  used: number;
  max_requests: number;
  window_days: number;
  window_start: string;
  remaining: number;
  auto_approve: boolean;
}
export interface AdminUserPlay {
  session_id: string;
  profile_id: string;
  profile_name: string;
  media_item_id: string;
  media_title: string;
  media_type: string;
  series_title: string;
  season_number: number | null;
  episode_number: number | null;
  play_method: string;
  started_at: string;
  ended_at: string;
  watched_seconds: number;
  duration_seconds: number | null;
  completed: boolean;
}
export interface AdminUserPlayPage {
  items: AdminUserPlay[];
  nextCursor: string | null;
}
export interface AdminUserDownload {
  id: string;
  profile_id: string;
  device_id: string;
  content_id: string;
  episode_id?: string;
  batch_id?: string;
  title: string;
  media_type: string;
  episode: { season_number: number; episode_number: number; title: string } | null;
  status: string;
  quality: string;
  effective_quality: string;
  delivery_format: string;
  target_bitrate_kbps: number;
  file_size: number;
  created_at: string;
  updated_at: string;
  completed_at: string | null;
  status_event_at: string | null;
}
export interface AdminUserDownloadSummary {
  total: number;
  completed: number;
  in_progress: number;
  failed: number;
  revoked: number;
  total_bytes: number;
  devices: number;
  monitored_series: number;
}
export interface AdminUserDownloadSubscription {
  id: string;
  profile_id: string;
  device_id: string;
  series_id: string;
  series_title: string;
  mode: string;
  season_numbers: number[];
  target_season: number | null;
  delete_watched: boolean;
  max_storage_bytes: number;
  active: boolean;
  on_device: number;
  in_progress: number;
  removed_episodes: number;
  created_at: string;
  updated_at: string;
}

// ---------------------------------------------------------------------------
// Wire checks. The generated types describe the contract; these guard the
// runtime shape so a malformed body fails loudly instead of rendering wrong.
// ---------------------------------------------------------------------------

type Row = Record<string, unknown>;

function invalid(what: string): never {
  throw new Error(`Invalid ${what} page. Reload the page.`);
}

function record(value: unknown, what: string): Row {
  if (typeof value !== "object" || value === null || Array.isArray(value)) invalid(what);
  return value as Row;
}
function text(row: Row, key: string, what: string): string {
  const value = row[key];
  if (typeof value !== "string") invalid(what);
  return value;
}
function nullableText(row: Row, key: string, what: string): string | null {
  return row[key] == null ? null : text(row, key, what);
}
function optionalText(row: Row, key: string, what: string): string {
  return nullableText(row, key, what) ?? "";
}
function count(row: Row, key: string, what: string): number {
  const value = row[key];
  if (typeof value !== "number" || !Number.isFinite(value)) invalid(what);
  return value;
}
function nullableCount(row: Row, key: string, what: string): number | null {
  return row[key] == null ? null : count(row, key, what);
}
function flag(row: Row, key: string, what: string): boolean {
  const value = row[key];
  if (typeof value !== "boolean") invalid(what);
  return value;
}
/** Opaque ids travel as strings; a numeric id is accepted and normalized. */
function id(row: Row, key: string, what: string): string {
  const value = row[key];
  if (typeof value === "number" && Number.isSafeInteger(value)) return String(value);
  if (typeof value !== "string") invalid(what);
  return value;
}

interface PageBody {
  items?: unknown;
  page?: { has_more?: unknown; next_cursor?: unknown } | null;
}

/**
 * Checks one page and returns its continuation. `seen` holds every cursor
 * already requested in this walk, so a server that loops is refused.
 */
function continuation(body: PageBody, what: string, seen: Set<string>): string | null {
  if (!Array.isArray(body.items) || !body.page || typeof body.page.has_more !== "boolean")
    invalid(what);
  const next = body.page.next_cursor;
  if (!body.page.has_more) {
    if (next) invalid(what);
    return null;
  }
  if (typeof next !== "string" || !next || seen.has(next)) invalid(what);
  return next;
}

/** A bounded collection must arrive complete in one page. */
function completeItems(body: PageBody, what: string): unknown[] {
  if (continuation(body, what, new Set()) !== null) invalid(what);
  return body.items as unknown[];
}

// ---------------------------------------------------------------------------
// Projections.
// ---------------------------------------------------------------------------

function deviceOf(value: unknown): AdminUserDeviceRow {
  const what = "device";
  const row = record(value, what);
  const profiles = row.profiles ?? [];
  if (!Array.isArray(profiles)) invalid(what);
  return {
    device_id: text(row, "device_id", what),
    device_name: optionalText(row, "device_name", what),
    device_platform: optionalText(row, "device_platform", what),
    last_seen_at: nullableText(row, "last_seen_at", what),
    last_updated: nullableText(row, "last_updated", what),
    override_count: count(row, "override_count", what),
    profiles: profiles.map((raw: unknown) => {
      const profile = record(raw, what);
      return {
        profile_id: id(profile, "profile_id", what),
        profile_name: optionalText(profile, "profile_name", what),
        override_count: count(profile, "override_count", what),
        last_seen_at: nullableText(profile, "last_seen_at", what),
      };
    }),
  };
}

function profileActivityOf(value: unknown): AdminUserProfileActivity {
  const what = "profile";
  const row = record(value, what);
  return {
    id: id(row, "id", what),
    name: text(row, "name", what),
    last_seen_at: nullableText(row, "last_seen_at", what),
  };
}

function playOf(value: unknown): AdminUserPlay {
  const what = "watch history";
  const row = record(value, what);
  const sessionID = text(row, "session_id", what);
  if (!sessionID) invalid(what);
  return {
    session_id: sessionID,
    profile_id: optionalText(row, "profile_id", what),
    profile_name: optionalText(row, "profile_name", what),
    media_item_id: optionalText(row, "media_item_id", what),
    media_title: optionalText(row, "media_title", what),
    media_type: optionalText(row, "media_type", what),
    series_title: optionalText(row, "series_title", what),
    season_number: nullableCount(row, "season_number", what),
    episode_number: nullableCount(row, "episode_number", what),
    play_method: optionalText(row, "play_method", what),
    started_at: text(row, "started_at", what),
    ended_at: text(row, "ended_at", what),
    watched_seconds: count(row, "watched_seconds", what),
    duration_seconds: nullableCount(row, "duration_seconds", what),
    completed: flag(row, "completed", what),
  };
}

function downloadOf(value: unknown): AdminUserDownload {
  const what = "download";
  const row = record(value, what);
  let episode: AdminUserDownload["episode"] = null;
  if (row.episode != null) {
    const raw = record(row.episode, what);
    episode = {
      season_number: count(raw, "season_number", what),
      episode_number: count(raw, "episode_number", what),
      title: optionalText(raw, "title", what),
    };
  }
  const episodeID = optionalText(row, "episode_id", what);
  const batchID = row.batch_id == null ? "" : id(row, "batch_id", what);
  return {
    id: id(row, "id", what),
    profile_id: id(row, "profile_id", what),
    device_id: text(row, "device_id", what),
    content_id: text(row, "content_id", what),
    ...(episodeID ? { episode_id: episodeID } : {}),
    ...(batchID ? { batch_id: batchID } : {}),
    title: optionalText(row, "title", what),
    media_type: optionalText(row, "media_type", what),
    episode,
    status: text(row, "status", what),
    quality: optionalText(row, "quality", what),
    effective_quality: optionalText(row, "effective_quality", what),
    delivery_format: optionalText(row, "delivery_format", what),
    target_bitrate_kbps: count(row, "target_bitrate_kbps", what),
    file_size: count(row, "file_size", what),
    created_at: text(row, "created_at", what),
    updated_at: text(row, "updated_at", what),
    completed_at: nullableText(row, "completed_at", what),
    status_event_at: nullableText(row, "status_event_at", what),
  };
}

function subscriptionOf(value: unknown): AdminUserDownloadSubscription {
  const what = "series monitor";
  const row = record(value, what);
  const seasons = row.season_numbers ?? [];
  if (!Array.isArray(seasons) || !seasons.every((s) => typeof s === "number")) invalid(what);
  return {
    id: id(row, "id", what),
    profile_id: id(row, "profile_id", what),
    device_id: text(row, "device_id", what),
    series_id: text(row, "series_id", what),
    series_title: optionalText(row, "series_title", what),
    mode: text(row, "mode", what),
    season_numbers: seasons as number[],
    target_season: nullableCount(row, "target_season", what),
    delete_watched: flag(row, "delete_watched", what),
    max_storage_bytes: count(row, "max_storage_bytes", what),
    active: flag(row, "active", what),
    on_device: count(row, "on_device", what),
    in_progress: count(row, "in_progress", what),
    removed_episodes: count(row, "removed_episodes", what),
    created_at: text(row, "created_at", what),
    updated_at: text(row, "updated_at", what),
  };
}

function sessionNumericID(raw: unknown): number {
  const value = typeof raw === "string" ? Number(raw) : NaN;
  if (!Number.isSafeInteger(value) || value < 0 || String(value) !== raw)
    throw new Error("Unsupported session identifier.");
  return value;
}

/** Same projection as `listAdminPlaybackSessions`: numeric ids for the views. */
function sessionOf(value: unknown): AdminSession {
  const row = record(value, "session") as Row & AdminSession;
  if (typeof row.session_id !== "string" || !row.session_id) invalid("session");
  const optionalID = (raw: unknown) => (raw == null ? undefined : sessionNumericID(raw));
  return {
    ...row,
    user_id: sessionNumericID(row.user_id),
    media_file_id: sessionNumericID(row.media_file_id),
    requested_media_file_id: sessionNumericID(row.requested_media_file_id),
    routing_execution_node_id: optionalID(row.routing_execution_node_id),
    routing_egress_node_id: optionalID(row.routing_egress_node_id),
  };
}

// ---------------------------------------------------------------------------
// Reads.
// ---------------------------------------------------------------------------

/** Sends one request, refused before and after it if the admin authority changed. */
async function asAdmin<T>(ctx: ProfileRequestContextSnapshot, request: () => Promise<T>) {
  requireAdminUserAuthority(ctx);
  const body = await request();
  requireAdminUserAuthority(ctx);
  return body;
}

/** Rows read up to a page cap; `truncated` means the server had more. */
export interface AdminUserList<T> {
  items: T[];
  truncated: boolean;
}

/** Walks pages up to `maxPages`, projecting each item, and keeps what it read. */
async function listUpTo<T>(
  ctx: ProfileRequestContextSnapshot,
  fetchPage: (cursor: string | undefined) => Promise<PageBody>,
  opts: { what: string; maxPages: number; project: (raw: unknown) => T },
): Promise<AdminUserList<T>> {
  const items: T[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  for (let pageIndex = 0; pageIndex < opts.maxPages; pageIndex++) {
    const body = await asAdmin(ctx, () => fetchPage(cursor));
    const next = continuation(body, opts.what, seen);
    items.push(...(body.items as unknown[]).map(opts.project));
    if (next === null) return { items, truncated: false };
    seen.add(next);
    cursor = next;
  }
  return { items, truncated: true };
}

/** Registered devices for the account, newest activity first (server order). */
export async function listAdminUserDevices(
  userId: number,
  ctx: ProfileRequestContextSnapshot,
  signal?: AbortSignal,
): Promise<AdminUserDeviceRow[]> {
  const body = await asAdmin(ctx, () =>
    v2("GET /api/v2/admin/users/{id}/devices", {
      path: { id: String(userId) },
      profileContext: ctx,
      signal,
    }),
  );
  return completeItems(body as PageBody, "device").map(deviceOf);
}

/** The account's profiles with the latest time any device reported each one. */
export async function listAdminUserProfileActivity(
  userId: number,
  ctx: ProfileRequestContextSnapshot,
  signal?: AbortSignal,
): Promise<AdminUserProfileActivity[]> {
  const body = await asAdmin(ctx, () =>
    v2("GET /api/v2/admin/users/{id}/profiles", {
      path: { id: String(userId) },
      profileContext: ctx,
      signal,
    }),
  );
  return completeItems(body as PageBody, "profile").map(profileActivityOf);
}

export async function getAdminUserWatchSummary(
  userId: number,
  q: { days: number; profileId?: string },
  ctx: ProfileRequestContextSnapshot,
  signal?: AbortSignal,
): Promise<AdminUserWatchSummary> {
  const body = await asAdmin(ctx, () =>
    v2("GET /api/v2/admin/users/{id}/watch-summary", {
      path: { id: String(userId) },
      query: { days: q.days, ...(q.profileId ? { profile_id: q.profileId } : {}) },
      profileContext: ctx,
      signal,
    }),
  );
  const what = "watch summary";
  const row = record(body, what);
  return {
    days: count(row, "days", what),
    since: text(row, "since", what),
    plays: count(row, "plays", what),
    completed_plays: count(row, "completed_plays", what),
    watched_seconds: count(row, "watched_seconds", what),
    last_played_at: nullableText(row, "last_played_at", what),
  };
}

export async function getAdminRequestUsage(
  userId: number,
  ctx: ProfileRequestContextSnapshot,
  signal?: AbortSignal,
): Promise<AdminRequestUsage> {
  const body = await asAdmin(ctx, () =>
    v2("GET /api/v2/admin/request-users/{user_id}/usage", {
      path: { user_id: String(userId) },
      profileContext: ctx,
      signal,
    }),
  );
  const what = "request usage";
  const row = record(body, what);
  return {
    requests_enabled: flag(row, "requests_enabled", what),
    allowed: flag(row, "allowed", what),
    unlimited: flag(row, "unlimited", what),
    used: count(row, "used", what),
    max_requests: count(row, "max_requests", what),
    window_days: count(row, "window_days", what),
    window_start: text(row, "window_start", what),
    remaining: count(row, "remaining", what),
    auto_approve: flag(row, "auto_approve", what),
  };
}

/**
 * Live playback observations for the account, up to ten pages of 100. Past
 * that the rows read so far are kept: 1,000 concurrent sessions is already far
 * beyond any stream limit, and a partial list beats an empty Overview.
 */
export async function listAdminUserLiveSessions(
  userId: number,
  ctx: ProfileRequestContextSnapshot,
  signal?: AbortSignal,
): Promise<AdminSession[]> {
  const fetchPage = (cursor: string | undefined) =>
    v2("GET /api/v2/admin/sessions", {
      query: { user_id: String(userId), limit: 100, cursor },
      profileContext: ctx,
      signal,
    }) as Promise<PageBody>;
  const { items: sessions } = await listUpTo(ctx, fetchPage, {
    what: "session",
    maxPages: 10,
    project: sessionOf,
  });
  // A session seen on two pages keeps its first position and latest state.
  return [...new Map(sessions.map((session) => [session.session_id, session])).values()];
}

/** The instant `days` days before `now`, as the RFC 3339 `ended_after` filter. */
export function endedAfterFor(days: number, now = Date.now()): string {
  return new Date(now - days * 86_400_000).toISOString();
}

/**
 * One page of the account's finalized plays that ended within the last
 * `days` days. The history cursor binds `ended_after`, so a caller paging
 * through one window passes the `endedAfter` it used for the first page.
 */
export async function listAdminUserPlays(
  q: {
    userId: number;
    profileId?: string;
    days: number;
    limit: number;
    cursor?: string;
    endedAfter?: string;
  },
  ctx: ProfileRequestContextSnapshot,
  signal?: AbortSignal,
): Promise<AdminUserPlayPage> {
  const body = await asAdmin(ctx, () =>
    v2("GET /api/v2/admin/playback-history", {
      query: {
        user_id: String(q.userId),
        ...(q.profileId ? { profile_id: q.profileId } : {}),
        ended_after: q.endedAfter ?? endedAfterFor(q.days),
        limit: q.limit,
        ...(q.cursor ? { cursor: q.cursor } : {}),
      },
      profileContext: ctx,
      signal,
    }),
  );
  const seen = new Set(q.cursor ? [q.cursor] : []);
  const next = continuation(body as PageBody, "watch history", seen);
  const items = (body.items as unknown[]).map(playOf);
  if (next !== null && items.length === 0) invalid("watch history");
  return { items, nextCursor: next };
}

/** Managed download rows on the account's devices, up to 25 pages of 200. */
export function listAllAdminUserDownloads(
  userId: number,
  ctx: ProfileRequestContextSnapshot,
  signal?: AbortSignal,
): Promise<AdminUserList<AdminUserDownload>> {
  const fetchPage = (cursor: string | undefined) =>
    v2("GET /api/v2/admin/users/{id}/downloads", {
      path: { id: String(userId) },
      query: { limit: 200, ...(cursor ? { cursor } : {}) },
      profileContext: ctx,
      signal,
    }) as Promise<PageBody>;
  return listUpTo(ctx, fetchPage, { what: "download", maxPages: 25, project: downloadOf });
}

export async function getAdminUserDownloadSummary(
  userId: number,
  ctx: ProfileRequestContextSnapshot,
  signal?: AbortSignal,
): Promise<AdminUserDownloadSummary> {
  const body = await asAdmin(ctx, () =>
    v2("GET /api/v2/admin/users/{id}/downloads/summary", {
      path: { id: String(userId) },
      profileContext: ctx,
      signal,
    }),
  );
  const what = "download summary";
  const row = record(body, what);
  return {
    total: count(row, "total", what),
    completed: count(row, "completed", what),
    in_progress: count(row, "in_progress", what),
    failed: count(row, "failed", what),
    revoked: count(row, "revoked", what),
    total_bytes: count(row, "total_bytes", what),
    devices: count(row, "devices", what),
    monitored_series: count(row, "monitored_series", what),
  };
}

/** Series monitors the account's devices synced, up to 10 pages of 200. */
export function listAllAdminUserDownloadSubscriptions(
  userId: number,
  ctx: ProfileRequestContextSnapshot,
  signal?: AbortSignal,
): Promise<AdminUserList<AdminUserDownloadSubscription>> {
  const fetchPage = (cursor: string | undefined) =>
    v2("GET /api/v2/admin/users/{id}/download-subscriptions", {
      path: { id: String(userId) },
      query: { limit: 200, ...(cursor ? { cursor } : {}) },
      profileContext: ctx,
      signal,
    }) as Promise<PageBody>;
  return listUpTo(ctx, fetchPage, {
    what: "series monitor",
    maxPages: 10,
    project: subscriptionOf,
  });
}
