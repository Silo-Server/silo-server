import type { AccessGroup, AdminUser, Library } from "@/api/types";
import type { PolicyInheritHints } from "@/components/UserPolicyFields";
import { playbackQualityPresetFromValue } from "@/lib/playback-quality";
import type { ResolvedRequestTerms } from "@/lib/requestAccess";
import { formatStreamBitrateLimit } from "@/lib/streamBitrateLimit";

/**
 * Where each Access & limits value comes from, and how the page words it.
 * An account's own override wins; otherwise a grouped non-admin account
 * inherits from its access group, and everyone else gets the server's fixed
 * no-group defaults (see `NO_GROUP_POLICY` in UserPolicyFields).
 */

export type ValueSource = "default" | "group" | "custom";
export type PolicyRowKey =
  | "libraries"
  | "maxQuality"
  | "maxStreams"
  | "videoTranscoding"
  | "audioTranscoding"
  | "remoteBitrate"
  | "localBitrate"
  | "downloads"
  | "serverPrepared"
  | "requests"
  | "maxProfiles";

/** "Owner", "Admin" or "User". */
export function roleLabel(user: Pick<AdminUser, "is_owner" | "role">): string {
  if (user.is_owner) return "Owner";
  return user.role === "admin" ? "Admin" : "User";
}

/** Where inherited values come from: "server" for admins and ungrouped accounts. */
export type InheritContext = { kind: "server" } | { kind: "group"; name: string };

/** The access group's display name, or "#id" while the group list lacks it. */
export function accessGroupName(groupId: number, groups: AccessGroup[]): string {
  return groups.find((group) => group.id === groupId)?.name ?? `#${groupId}`;
}

export function inheritContextFor(user: AdminUser, groups: AccessGroup[]): InheritContext {
  if (user.role === "admin" || user.access_group_id === null) return { kind: "server" };
  return { kind: "group", name: accessGroupName(user.access_group_id, groups) };
}

/** Whether the account overrides the row: any non-null override field. */
function rowOverridden(user: AdminUser, row: PolicyRowKey): boolean {
  switch (row) {
    case "libraries":
      return user.library_ids !== null;
    case "maxQuality":
      return user.max_playback_quality !== null;
    case "maxStreams":
      return user.max_streams !== null;
    case "videoTranscoding":
      return user.transcode_allowed !== null || user.max_transcodes !== null;
    case "audioTranscoding":
      return user.audio_transcode_allowed !== null;
    case "remoteBitrate":
      return user.max_remote_stream_bitrate_kbps !== null;
    case "localBitrate":
      return user.max_local_stream_bitrate_kbps !== null;
    case "downloads":
      return user.download_allowed !== null;
    case "serverPrepared":
      return user.download_transcode_allowed !== null;
    case "requests":
      return user.requests_allowed !== null;
    case "maxProfiles":
      return user.max_profiles !== null;
  }
}

export function rowSource(user: AdminUser, row: PolicyRowKey, ctx: InheritContext): ValueSource {
  if (rowOverridden(user, row)) return "custom";
  return ctx.kind === "group" ? "group" : "default";
}

/** "Default" or "Group", the prefix of an inherited value. */
function inheritPrefix(ctx: InheritContext): string {
  return ctx.kind === "group" ? "Group" : "Default";
}

export function libraryListText(ids: number[] | null, libraries: Library[]): string {
  if (ids === null) return "All libraries";
  if (ids.length === 0) return "No libraries";
  return ids
    .map((id) => libraries.find((library) => library.id === id)?.name ?? `#${id}`)
    .join(", ");
}

/** The cap as a resolution ("1080p", "4K"), or "Any". */
export function formatQuality(value: string | null | undefined): string {
  switch (playbackQualityPresetFromValue(value)) {
    case "standard":
      return "1080p";
    case "4k":
      return "4K";
    default:
      return "Any";
  }
}

/** Lower-cases the generic words of a value for use after "Default: " (names keep their case). */
function lowerFirst(text: string): string {
  return /^[A-Z][a-z]/.test(text) ? text.charAt(0).toLowerCase() + text.slice(1) : text;
}

function inheritedVideoText(allowed: boolean, max: number): string {
  if (!allowed) return "not allowed";
  return max === 0 ? "allowed, unlimited" : `allowed, up to ${max} at a time`;
}

/** "Default: unlimited" / "Group: 2"; undefined when the inherited value is unknown. */
export function inheritedValueText(
  row: PolicyRowKey,
  hints: PolicyInheritHints | undefined,
  ctx: InheritContext,
  libraries: Library[],
): string | undefined {
  const value = inheritedValue(row, hints, libraries);
  return value === undefined ? undefined : `${inheritPrefix(ctx)}: ${value}`;
}

/** The formatted value lower-cased for use after a prefix; undefined while unknown. */
function lowered<T>(value: T | undefined, format: (value: T) => string): string | undefined {
  return value === undefined ? undefined : lowerFirst(format(value));
}

/** The inherited value alone, lower-cased as it reads after a prefix. */
function inheritedValue(
  row: PolicyRowKey,
  hints: PolicyInheritHints | undefined,
  libraries: Library[],
): string | undefined {
  if (!hints) return undefined;
  switch (row) {
    case "libraries":
      if (hints.library_ids === undefined) return undefined;
      // Library names keep their case; only "All libraries" and "No libraries" lower.
      return hints.library_ids === null || hints.library_ids.length === 0
        ? lowerFirst(libraryListText(hints.library_ids, libraries))
        : libraryListText(hints.library_ids, libraries);
    case "maxQuality":
      return lowered(hints.max_playback_quality, formatQuality);
    case "maxStreams":
      return lowered(hints.max_streams, formatStreams);
    case "videoTranscoding":
      return hints.transcode_allowed === undefined || hints.max_transcodes === undefined
        ? undefined
        : inheritedVideoText(hints.transcode_allowed, hints.max_transcodes);
    case "audioTranscoding":
      return lowered(hints.audio_transcode_allowed, formatAllowed);
    case "remoteBitrate":
      return lowered(hints.max_remote_stream_bitrate_kbps, formatBitrateCap);
    case "localBitrate":
      return lowered(hints.max_local_stream_bitrate_kbps, formatBitrateCap);
    case "downloads":
      return lowered(hints.download_allowed, formatAllowed);
    case "serverPrepared":
      return lowered(hints.download_transcode_allowed, formatAllowed);
    case "requests":
      return lowered(hints.requests_allowed, (allowed) => (allowed ? "yes" : "no"));
    case "maxProfiles":
      return lowered(hints.max_profiles, String);
  }
}

const POLICY_ROWS: PolicyRowKey[] = [
  "libraries",
  "maxQuality",
  "maxStreams",
  "videoTranscoding",
  "audioTranscoding",
  "remoteBitrate",
  "localBitrate",
  "downloads",
  "serverPrepared",
  "requests",
  "maxProfiles",
];

/** Rows this account sets itself; video transcoding counts once for its two fields. */
export function countCustomPolicyRows(user: AdminUser): number {
  return countCustomRows(user, POLICY_ROWS);
}

/**
 * Request approval and limit set on the account itself. They live in the
 * account's request-limit record, not on AdminUser, so countCustomPolicyRows
 * can't see them.
 */
export function countCustomRequestTerms(terms: ResolvedRequestTerms | undefined): number {
  if (!terms) return 0;
  return [terms.quotaSource, terms.approvalSource].filter((source) => source.kind === "account")
    .length;
}

export function countCustomRows(user: AdminUser, rows: readonly PolicyRowKey[]): number {
  return rows.filter((row) => rowOverridden(user, row)).length;
}

export type VideoTranscoding =
  | { mode: "off" }
  | { mode: "unlimited" }
  | { mode: "limit"; max: number };

/**
 * The account's own video transcoding setting; null when both fields follow
 * the default. The rare "inherit on/off, override the count" state reads as a
 * custom value too; the card shows it from the effective policy.
 */
export function videoTranscodingFromOverrides(
  allowed: boolean | null,
  max: number | null,
): VideoTranscoding | null {
  if (allowed === null && max === null) return null;
  if (allowed === false) return { mode: "off" };
  if (max === null || max === 0) return { mode: "unlimited" };
  return { mode: "limit", max };
}

export function videoTranscodingFromEffective(allowed: boolean, max: number): VideoTranscoding {
  if (!allowed) return { mode: "off" };
  return max === 0 ? { mode: "unlimited" } : { mode: "limit", max };
}

/** Off → false/null; Unlimited → true/0; Up to N → true/N; Default → null/null. */
export function videoTranscodingOverrides(v: VideoTranscoding | null): {
  transcode_allowed: boolean | null;
  max_transcodes: number | null;
} {
  if (v === null) return { transcode_allowed: null, max_transcodes: null };
  switch (v.mode) {
    case "off":
      return { transcode_allowed: false, max_transcodes: null };
    case "unlimited":
      return { transcode_allowed: true, max_transcodes: 0 };
    case "limit":
      return { transcode_allowed: true, max_transcodes: v.max };
  }
}

export function sameVideoTranscoding(
  a: VideoTranscoding | null,
  b: VideoTranscoding | null,
): boolean {
  if (a === null || b === null) return a === b;
  if (a.mode === "limit" && b.mode === "limit") return a.max === b.max;
  return a.mode === b.mode;
}

export function formatVideoTranscoding(v: VideoTranscoding): string {
  switch (v.mode) {
    case "off":
      return "Off";
    case "unlimited":
      return "Unlimited";
    case "limit":
      return `Up to ${v.max} at a time`;
  }
}

export function formatStreams(n: number): string {
  return n === 0 ? "Unlimited" : String(n);
}

export function formatBitrateCap(kbps: number): string {
  return kbps === 0 ? "No cap" : formatStreamBitrateLimit(kbps);
}

export function formatAllowed(b: boolean): string {
  return b ? "Allowed" : "Not allowed";
}

/**
 * Whether the account's access group keeps it from using a permission: the
 * server intersects the account's permissions with the group's allowed set,
 * so an assigned permission can still have no effect.
 */
export function permissionLock(
  user: AdminUser,
  groups: AccessGroup[],
  permission: string,
): { locked: boolean; groupName?: string } {
  if (user.role === "admin" || user.access_group_id === null) return { locked: false };
  const group = groups.find((candidate) => candidate.id === user.access_group_id);
  if (!group) return { locked: false };
  const locked =
    group.allowed_permissions !== null && !group.allowed_permissions.includes(permission);
  return { locked, groupName: group.name };
}

/**
 * One policy row in an edit: Default (no override) or Custom with a value.
 * Custom without a value yet (a cleared box, a choice not made) keeps the
 * row on its default until the admin enters one; `text` holds what was typed.
 */
export interface RowDraft<T> {
  custom: boolean;
  value: T | null;
  text?: string;
}

export function rowDraft<T>(override: T | null, text?: string): RowDraft<T> {
  return { custom: override !== null, value: override, ...(text === undefined ? {} : { text }) };
}

/** What the row saves: null for Default, undefined while Custom has no valid value. */
export function rowOverride<T>(row: RowDraft<T>): T | null | undefined {
  if (!row.custom) return null;
  return row.value === null ? undefined : row.value;
}

/**
 * Whether the row differs from the saved override. An incomplete Custom row
 * counts only when it replaces a saved override (it then has to be fixed).
 */
export function rowChanged<T>(
  row: RowDraft<T>,
  saved: T | null,
  equal: (a: T, b: T) => boolean = Object.is,
): boolean {
  const next = rowOverride(row);
  if (next === undefined) return saved !== null;
  if (next === null || saved === null) return next !== saved;
  return !equal(next, saved);
}

/** A whole number at or above `min`, or null. */
export function parseWholeNumber(text: string, min: number): number | null {
  const trimmed = text.trim();
  if (!/^\d+$/.test(trimmed)) return null;
  const n = Number(trimmed);
  return Number.isSafeInteger(n) && n >= min ? n : null;
}

export const ALLOWED_OPTIONS: { value: boolean; label: string }[] = [
  { value: true, label: "Allowed" },
  { value: false, label: "Not allowed" },
];
