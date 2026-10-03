import type { AccessGroup } from "@/api/types";
import { PLAYBACK_QUALITY_OPTIONS, playbackQualityPresetFromValue } from "@/lib/playback-quality";
import { formatStreamBitrateLimit } from "@/lib/streamBitrateLimit";

/** The group settings members inherit, which is what a move compares. */
export type GroupPolicy = Pick<
  AccessGroup,
  | "library_ids"
  | "max_playback_quality"
  | "download_allowed"
  | "download_transcode_allowed"
  | "transcode_allowed"
  | "audio_transcode_allowed"
  | "max_streams"
  | "max_transcodes"
  | "max_remote_stream_bitrate_kbps"
  | "max_local_stream_bitrate_kbps"
  | "allowed_permissions"
  | "requests_allowed"
>;

// Mirrors access.NoGroupPolicy(): what an account in no group inherits, so a
// move out of no group lists what it loses. Keep in sync with
// internal/access/groups.go.
export const NO_GROUP_POLICY: GroupPolicy = {
  library_ids: null,
  max_playback_quality: "",
  download_allowed: true,
  download_transcode_allowed: false,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 0,
  max_transcodes: 0,
  max_remote_stream_bitrate_kbps: 0,
  max_local_stream_bitrate_kbps: 0,
  allowed_permissions: null,
  requests_allowed: true,
};

export interface PolicyChange {
  label: string;
  from: string;
  to: string;
}

function limit(value: number) {
  return value > 0 ? String(value) : "Unlimited";
}

// Whole-kbps precision: 128 and 64 kbps must not both read as 0.1 Mbps.
function bitrate(kbps: number) {
  return kbps <= 0 ? "Unlimited" : formatStreamBitrateLimit(kbps);
}

function allowed(value: boolean) {
  return value ? "Allowed" : "Not allowed";
}

function libraries(ids: number[] | null, names: ReadonlyMap<number, string>) {
  if (ids === null) return "All libraries";
  if (ids.length === 0) return "No libraries";
  return ids.map((id) => names.get(id) ?? `#${id}`).join(", ");
}

function permissions(values: string[] | null) {
  if (values === null) return "All";
  return values.length === 0 ? "None" : [...values].sort().join(", ");
}

function quality(value: string) {
  const preset = playbackQualityPresetFromValue(value);
  return PLAYBACK_QUALITY_OPTIONS.find((option) => option.value === preset)?.label ?? value;
}

function sameIds(a: number[] | null, b: number[] | null) {
  if (a === null || b === null) return a === b;
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((id) => set.has(id));
}

/** The group policies that differ between two groups, as members inheriting them see it. */
export function groupPolicyChanges(
  from: GroupPolicy,
  to: GroupPolicy,
  libraryNames: ReadonlyMap<number, string> = new Map(),
): PolicyChange[] {
  const changes: PolicyChange[] = [];
  const add = (label: string, a: string, b: string) => {
    if (a !== b) changes.push({ label, from: a, to: b });
  };
  if (!sameIds(from.library_ids, to.library_ids)) {
    changes.push({
      label: "Libraries",
      from: libraries(from.library_ids, libraryNames),
      to: libraries(to.library_ids, libraryNames),
    });
  }
  add("Playback quality", quality(from.max_playback_quality), quality(to.max_playback_quality));
  add("Downloads", allowed(from.download_allowed), allowed(to.download_allowed));
  add(
    "Converted downloads",
    allowed(from.download_transcode_allowed),
    allowed(to.download_transcode_allowed),
  );
  add("Video transcoding", allowed(from.transcode_allowed), allowed(to.transcode_allowed));
  add(
    "Audio transcoding",
    allowed(from.audio_transcode_allowed),
    allowed(to.audio_transcode_allowed),
  );
  add("Concurrent streams", limit(from.max_streams), limit(to.max_streams));
  add("Concurrent transcodes", limit(from.max_transcodes), limit(to.max_transcodes));
  add(
    "Remote stream bitrate",
    bitrate(from.max_remote_stream_bitrate_kbps),
    bitrate(to.max_remote_stream_bitrate_kbps),
  );
  add(
    "Local stream bitrate",
    bitrate(from.max_local_stream_bitrate_kbps),
    bitrate(to.max_local_stream_bitrate_kbps),
  );
  add("Permissions", permissions(from.allowed_permissions), permissions(to.allowed_permissions));
  add("Requests", allowed(from.requests_allowed), allowed(to.requests_allowed));
  return changes;
}
