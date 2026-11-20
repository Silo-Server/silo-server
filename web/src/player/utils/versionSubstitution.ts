/**
 * Decides whether a playback plan really substituted one release for another.
 *
 * The server can move the effective row to a sibling mirror of the very same
 * release (a dedup keeper, a rotation, or another provider serving identical
 * bytes). Comparing only catalog row ids reports that as a substitution and
 * shows the viewer a false "different version" notice, so the decision compares
 * release identity instead:
 *
 * - An explicit `substituted_from_file_id` is the server's own substitution
 *   signal. It is authoritative unless the two rows prove they are the same
 *   release.
 * - Without that signal, two rows that identify different releases are still a
 *   real substitution when the effective identity moved.
 * - Anything else is unknown. Unknown never renders a notice; claiming a
 *   substitution the client cannot prove is worse than saying nothing.
 */

/** The release-identity fields both the player and catalog version rows carry. */
export interface ReleaseIdentityRow {
  file_id: number;
  file_path?: string;
  container?: string;
  release_name?: string;
  file_size?: number;
  resolution?: string;
  codec_video?: string;
  codec_audio?: string;
  hdr?: boolean;
  edition_key?: string;
  presentation_group_key?: string;
  presentation_part_index?: number;
}

/** Whether two rows describe the same release, different releases, or neither. */
export type ReleaseIdentityMatch = "same" | "different" | "unknown";

export interface VersionSubstitutionInput<T extends ReleaseIdentityRow> {
  /** The catalog row the viewer asked for, or null when no plan named one. */
  requestedVersion: T | undefined;
  /** The row the live session resolves as effective, when it can be resolved. */
  effectiveVersion: T | undefined;
  /**
   * True when the effective identity moved off the requested one: a different
   * effective file id, or a resolved virtual candidate whose path differs from
   * the requested row's path. Without this there is nothing to substitute.
   */
  identitiesDiffer: boolean;
  /** The plan's additive `substituted_from_file_id`, when the server sent one. */
  substitutedFromFileId?: number | null;
}

export interface VersionSubstitutionVerdict {
  substituted: boolean;
  releaseMatch: ReleaseIdentityMatch;
}

/**
 * A neutral virtual row names a provider search, not a release of its own. A
 * concrete candidate row also lives under `virtual://`, but its path carries
 * the resolved `result=` token the search path lacks.
 */
function isNeutralVirtualRow(row: ReleaseIdentityRow): boolean {
  const path = row.file_path?.trim();
  if (!path || !path.toLowerCase().startsWith("virtual://")) return false;
  try {
    return !new URL(path, "http://silo.local").searchParams.has("result");
  } catch {
    return !/[?&]result=/.test(path);
  }
}

function positive(value: number | undefined): value is number {
  return value != null && Number.isFinite(value) && value > 0;
}

/**
 * Whether the shared fields that are present on both rows agree. A field only
 * one row carries says nothing either way, so it is skipped rather than treated
 * as a difference.
 */
function sharedFieldsAgree(a: ReleaseIdentityRow, b: ReleaseIdentityRow): boolean {
  if (a.resolution !== undefined && b.resolution !== undefined && a.resolution !== b.resolution) {
    return false;
  }
  if (
    a.codec_video !== undefined &&
    b.codec_video !== undefined &&
    a.codec_video !== b.codec_video
  ) {
    return false;
  }
  if (
    a.codec_audio !== undefined &&
    b.codec_audio !== undefined &&
    a.codec_audio !== b.codec_audio
  ) {
    return false;
  }
  if (a.hdr !== undefined && b.hdr !== undefined && a.hdr !== b.hdr) {
    return false;
  }
  if (a.container !== undefined && b.container !== undefined && a.container !== b.container) {
    return false;
  }
  if (
    a.edition_key !== undefined &&
    b.edition_key !== undefined &&
    a.edition_key !== b.edition_key
  ) {
    return false;
  }
  if (
    a.presentation_group_key !== undefined &&
    b.presentation_group_key !== undefined &&
    a.presentation_group_key !== b.presentation_group_key
  ) {
    return false;
  }
  if (
    a.presentation_part_index !== undefined &&
    b.presentation_part_index !== undefined &&
    a.presentation_part_index !== b.presentation_part_index
  ) {
    return false;
  }
  return true;
}

/**
 * Classifies two catalog rows as the same release, different releases, or
 * unknown.
 *
 * A release name is the strongest identity. Without one, the provider file size
 * is the next strongest evidence: a mirror of one release serves identical
 * bytes, while another release almost never shares both the byte count and the
 * encode fingerprint. Two rows the client cannot describe are unknown.
 */
export function compareReleaseIdentity(
  requested: ReleaseIdentityRow | undefined,
  effective: ReleaseIdentityRow | undefined,
): ReleaseIdentityMatch {
  if (!requested || !effective) return "unknown";
  if (requested.file_id === effective.file_id) return "same";
  // The neutral row carries the request's picker metadata, not the bytes the
  // resolver chose, so it cannot describe the effective release.
  if (isNeutralVirtualRow(requested) || isNeutralVirtualRow(effective)) return "unknown";

  const requestedName = requested.release_name?.trim();
  const effectiveName = effective.release_name?.trim();
  if (requestedName && effectiveName) {
    return requestedName === effectiveName ? "same" : "different";
  }

  if (positive(requested.file_size) && positive(effective.file_size)) {
    if (requested.file_size !== effective.file_size) return "different";
    return sharedFieldsAgree(requested, effective) ? "same" : "different";
  }

  return "unknown";
}

/**
 * The one place the player decides whether to tell the viewer a version was
 * substituted. Same-release movement stays quiet; an explicit server signal or
 * two provably different releases may speak; anything unknown stays quiet.
 */
export function resolveVersionSubstitution<T extends ReleaseIdentityRow>({
  requestedVersion,
  effectiveVersion,
  identitiesDiffer,
  substitutedFromFileId,
}: VersionSubstitutionInput<T>): VersionSubstitutionVerdict {
  if (!identitiesDiffer) {
    return { substituted: false, releaseMatch: "same" };
  }
  const releaseMatch = compareReleaseIdentity(requestedVersion, effectiveVersion);
  if (releaseMatch === "same") {
    return { substituted: false, releaseMatch };
  }
  if (substitutedFromFileId != null) {
    return { substituted: true, releaseMatch };
  }
  return { substituted: releaseMatch === "different", releaseMatch };
}
