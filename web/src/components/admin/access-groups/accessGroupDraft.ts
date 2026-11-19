import type { AccessGroup, AccessGroupInput } from "@/api/types";
import {
  playbackQualityPresetFromValue,
  playbackQualityValueFromPreset,
} from "@/lib/playback-quality";

/**
 * The body a save would send for the group as stored, built with the same
 * normalization as the editor's (quality preset, clamped transcoded
 * downloads), so an untouched draft compares equal to it.
 */
export function storedGroupBody(group: AccessGroup): AccessGroupInput {
  return {
    name: group.name.trim(),
    description: group.description.trim(),
    library_ids: group.library_ids,
    max_playback_quality: playbackQualityValueFromPreset(
      playbackQualityPresetFromValue(group.max_playback_quality),
    ),
    download_allowed: group.download_allowed,
    download_transcode_allowed: group.download_allowed && group.download_transcode_allowed,
    transcode_allowed: group.transcode_allowed,
    audio_transcode_allowed: group.audio_transcode_allowed,
    max_streams: group.max_streams,
    max_transcodes: group.max_transcodes,
    max_remote_stream_bitrate_kbps: group.max_remote_stream_bitrate_kbps,
    max_local_stream_bitrate_kbps: group.max_local_stream_bitrate_kbps,
    max_profiles: group.max_profiles,
    allowed_permissions: group.allowed_permissions,
    requests_allowed: group.requests_allowed,
    is_default: group.is_default,
  };
}

// Sets, not sequences: the library picker and the permission toggles reorder
// them, and a reordering alone is not an edit worth a group write.
const UNORDERED_FIELDS: ReadonlySet<keyof AccessGroupInput> = new Set([
  "library_ids",
  "allowed_permissions",
]);

function comparable(key: keyof AccessGroupInput, value: unknown): string {
  return JSON.stringify(
    UNORDERED_FIELDS.has(key) && Array.isArray(value) ? [...value].sort() : value,
  );
}

export function sameGroupBody(a: AccessGroupInput, b: AccessGroupInput): boolean {
  return (Object.keys(b) as Array<keyof AccessGroupInput>).every(
    (key) => comparable(key, a[key]) === comparable(key, b[key]),
  );
}

/** The description is one line: line breaks typed or pasted on a phone become spaces. */
export function singleLine(value: string): string {
  return value.replace(/\r\n|[\r\n]/g, " ");
}
