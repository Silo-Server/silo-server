import type { FileVersion, LeafItemUserData, PlaybackVariant } from "@/api/types";
import { audioTrackAtmosEvidence, mapAudioLabel } from "@/lib/mediaFormat";
import { versionAudioAtmosEvidence } from "./versionFormatUtils";

const RESOLUTION_RANK: Record<string, number> = {
  "4k": 4,
  "2160p": 4,
  "1440p": 3,
  "1080p": 2,
  "720p": 1,
  "480p": 0,
};

const AUDIO_RANK: Record<string, number> = {
  atmos: 6,
  truehd: 5,
  "dts-hd": 4,
  "dts:x": 4,
  dts: 3,
  flac: 2,
  eac3: 1,
  "e-ac-3": 1,
  aac: 0,
};

export function resolutionScore(res: string): number {
  return RESOLUTION_RANK[res.toLowerCase()] ?? -1;
}

export function audioScore(codec: string): number {
  const lower = codec.toLowerCase();
  for (const [key, score] of Object.entries(AUDIO_RANK)) {
    if (lower.includes(key)) return score;
  }
  return -1;
}

function normalizeEditionTokens(variant: PlaybackVariant): string[] {
  return [variant.edition_key, variant.edition_raw]
    .map((value) => value?.trim().toLowerCase())
    .filter((value): value is string => Boolean(value));
}

export function playbackVariantEditionPreference(variant: PlaybackVariant): number {
  const values = normalizeEditionTokens(variant);
  if (values.length === 0) {
    return 0;
  }

  if (
    values.some(
      (value) =>
        value === "standard" ||
        value === "default" ||
        value.startsWith("standard ") ||
        value.endsWith(" standard"),
    )
  ) {
    return 0;
  }

  if (values.some((value) => value.includes("theatrical"))) {
    return 1;
  }

  return 2;
}

export function sortPlaybackVariantsByEditionPreference(
  playbackVariants: PlaybackVariant[],
): PlaybackVariant[] {
  return playbackVariants
    .map((variant, index) => ({ variant, index }))
    .sort(
      (a, b) =>
        playbackVariantEditionPreference(a.variant) - playbackVariantEditionPreference(b.variant) ||
        a.index - b.index,
    )
    .map(({ variant }) => variant);
}

export function pickBestAttributes(
  versions: FileVersion[],
  qualityPreference?: string | null,
): {
  resolution: string;
  hdr: boolean;
  audioLabel: string;
} | null {
  const best = bestAttributes(versions, qualityPreference);
  if (!best) return null;
  return {
    resolution: best.resolution,
    hdr: best.hdr,
    audioLabel: best.audioCodec ? mapAudioLabel(best.audioCodec, best.audioEvidence) : "",
  };
}

// bestAttributes ranks audio by codec alone; the profile and titles only
// refine the displayed label, so they never change which version is preferred.
function bestAttributes(
  versions: FileVersion[],
  qualityPreference?: string | null,
): {
  resolution: string;
  hdr: boolean;
  audioCodec: string;
  audioEvidence?: string;
} | null {
  if (versions.length === 0) return null;

  // When the user has a quality preference, only consider versions at or below that cap.
  let candidates = versions;
  if (qualityPreference && qualityPreference !== "auto") {
    const maxRank = RESOLUTION_RANK[qualityPreference.toLowerCase()] ?? -1;
    if (maxRank >= 0) {
      const filtered = versions.filter((v) => resolutionScore(v.resolution) <= maxRank);
      if (filtered.length > 0) candidates = filtered;
    }
  }

  let bestRes = "";
  let bestResScore = -1;
  let hdr = false;
  let bestAudioCodec = "";
  let bestAudioEvidence: string | undefined;
  let bestAudioScore = -1;

  // On equal codec scores an Atmos track supplies the label. Equal scores are
  // the same codec family, so the codec-only label used to pick a version
  // does not change.
  const replacesBestAudio = (score: number, codec: string, evidence?: string) =>
    score > bestAudioScore ||
    (score >= 0 &&
      score === bestAudioScore &&
      !mapAudioLabel(bestAudioCodec, bestAudioEvidence).includes("Atmos") &&
      mapAudioLabel(codec, evidence).includes("Atmos"));

  for (const v of candidates) {
    const rs = resolutionScore(v.resolution);
    if (rs > bestResScore) {
      bestResScore = rs;
      bestRes = v.resolution;
    }
    if (v.hdr) hdr = true;

    const as_ = audioScore(v.codec_audio);
    const versionEvidence = versionAudioAtmosEvidence(v);
    if (replacesBestAudio(as_, v.codec_audio, versionEvidence)) {
      bestAudioScore = as_;
      bestAudioCodec = v.codec_audio;
      bestAudioEvidence = versionEvidence;
    }

    // Also check individual audio tracks for higher-quality codecs
    if (v.audio_tracks) {
      for (const t of v.audio_tracks) {
        if (t.codec) {
          const ts = audioScore(t.codec);
          const trackEvidence = audioTrackAtmosEvidence(t);
          if (replacesBestAudio(ts, t.codec, trackEvidence)) {
            bestAudioScore = ts;
            bestAudioCodec = t.codec;
            bestAudioEvidence = trackEvidence;
          }
        }
      }
    }
  }

  return {
    resolution: bestRes,
    hdr,
    audioCodec: bestAudioCodec,
    audioEvidence: bestAudioEvidence,
  };
}

/**
 * Pick the best default version based on user watch history, quality preference,
 * and resolution ranking.
 */
export function selectDefaultVersion(
  versions: FileVersion[],
  userData?: LeafItemUserData,
  qualityPreference?: string | null,
  preferredEditionKey?: string | null,
): FileVersion | null {
  if (versions.length === 0) return null;

  const effectiveEditionKey = preferredEditionKey ?? userData?.last_edition_key;

  // Prefer the version they last watched, even when the enrichment refresh has
  // changed its edition metadata. A persisted pick that still exists must not
  // be silently replaced by another row because an edition key moved.
  if (userData?.last_file_id != null) {
    const matching = versions.find((v) => v.file_id === userData.last_file_id);
    if (matching) {
      return matching;
    }
  }

  const candidates =
    effectiveEditionKey && versions.some((v) => v.edition_key === effectiveEditionKey)
      ? versions.filter((v) => v.edition_key === effectiveEditionKey)
      : versions;

  if (candidates.length === 1) return candidates[0] ?? null;

  // Use quality-preference ranking to find the best match.
  const preferred = bestAttributes(candidates, qualityPreference);
  if (preferred) {
    const preferredAudio = preferred.audioCodec ? mapAudioLabel(preferred.audioCodec) : "";
    const matching = candidates.find(
      (v) =>
        v.resolution === preferred.resolution &&
        v.hdr === preferred.hdr &&
        (preferredAudio === "" ||
          mapAudioLabel(v.codec_audio).toLowerCase() === preferredAudio.toLowerCase()),
    );
    if (matching) return matching;
  }

  // Fall back to highest resolution.
  const sorted = [...candidates].sort(
    (a, b) => resolutionScore(b.resolution) - resolutionScore(a.resolution),
  );
  return sorted[0] ?? null;
}

/** Why the item page could not keep the version row it preferred. */
export type VersionFallbackReason = "unlisted" | "failed" | "unavailable";

/** The row Play will start, and why a different row than the preferred one. */
export interface ItemVersionSelection {
  version: FileVersion | null;
  /**
   * Set only when the page could not play the row it preferred. `skipped` is
   * null when that row is no longer in the list at all; `selected` is the row
   * Play will start, which equals `skipped` when no other row is playable.
   */
  fallback: {
    reason: VersionFallbackReason;
    skipped: FileVersion | null;
    selected: FileVersion | null;
  } | null;
}

function versionPlayable(version: FileVersion): boolean {
  return version.failed !== true && version.available !== false;
}

function unplayableReason(version: FileVersion): VersionFallbackReason {
  return version.failed === true ? "failed" : "unavailable";
}

/**
 * Pick the row the item page plays.
 *
 * Three situations used to dead-end the page, each with the Play button
 * disabled and no way forward:
 *
 *  - the persisted row (`last_file_id`, or the device's own pick) is not in the
 *    list, which is what a plan that named an effective row outside the version
 *    list leaves behind;
 *  - the row the server nominates as default is marked failed;
 *  - the row the server nominates as default is unavailable right now.
 *
 * None of them means the item has nothing to play. An explicit pick the viewer
 * made on this device is always kept, even when the liveness check reports it
 * failed: Play re-links that row instead of quietly choosing another release.
 * Any other preference (the server's `last_file_id`, or the server's default
 * row) is skipped when it is failed or unavailable, and the page reports why so
 * the version list stays one click away.
 */
export function selectItemVersion(
  versions: FileVersion[],
  defaultVersion: FileVersion | null,
  persistedFileId: number | null,
  explicitPickId: number | null = null,
): ItemVersionSelection {
  if (versions.length === 0) {
    return { version: defaultVersion, fallback: null };
  }
  const explicitPick =
    explicitPickId != null
      ? versions.find((version) => version.file_id === explicitPickId)
      : undefined;
  if (explicitPick) {
    return { version: explicitPick, fallback: null };
  }
  const persisted =
    persistedFileId != null
      ? versions.find((version) => version.file_id === persistedFileId)
      : undefined;
  const preferred = persisted ?? inListVersion(versions, defaultVersion) ?? versions[0] ?? null;
  if (preferred && versionPlayable(preferred)) {
    return {
      version: preferred,
      // A persisted row that left the list is still worth saying out loud: the
      // viewer asked for it, and the page is about to play something else.
      fallback:
        persisted == null && persistedFileId != null
          ? { reason: "unlisted", skipped: null, selected: preferred }
          : null,
    };
  }
  const replacement =
    versions.find(
      (version) => version.file_id !== preferred?.file_id && versionPlayable(version),
    ) ?? null;
  if (replacement) {
    return {
      version: replacement,
      fallback: {
        reason: preferred ? unplayableReason(preferred) : "unavailable",
        skipped: preferred,
        selected: replacement,
      },
    };
  }
  // Nothing is playable. Keep the preferred row anyway: starting it retries the
  // version with a fresh link, which beats a dead page when the provider has
  // recovered since the liveness check.
  return {
    version: preferred,
    fallback: preferred
      ? {
          reason: unplayableReason(preferred),
          skipped: preferred,
          selected: preferred,
        }
      : null,
  };
}

function inListVersion(
  versions: FileVersion[],
  version: FileVersion | null,
): FileVersion | undefined {
  if (!version) return undefined;
  return versions.find((row) => row.file_id === version.file_id);
}

export function selectDefaultPlaybackVariantVersion(
  versions: FileVersion[],
  playbackVariants: PlaybackVariant[] | undefined,
  userData?: LeafItemUserData,
  qualityPreference?: string | null,
  preferredEditionKey?: string | null,
  requestedFileId?: number | null,
): FileVersion | null {
  // The viewer's own request outranks every server hint: progress persistence
  // records the row that actually served the bytes, so `last_file_id` can name
  // a substitute for the neutral or requested row they picked. Keep the request
  // while the catalog still carries it; a row it dropped stays missing so the
  // page asks for a new pick rather than choosing silently.
  if (requestedFileId != null) {
    const requested = versions.find((version) => version.file_id === requestedFileId);
    if (requested) {
      return requested;
    }
  }

  if (!playbackVariants || playbackVariants.length === 0) {
    return selectDefaultVersion(versions, userData, qualityPreference, preferredEditionKey);
  }

  const effectiveEditionKey = preferredEditionKey ?? userData?.last_edition_key;
  const rankedVariants = sortPlaybackVariantsByEditionPreference(playbackVariants);
  let candidateVariants = rankedVariants;
  if (
    effectiveEditionKey &&
    rankedVariants.some((variant) => variant.edition_key === effectiveEditionKey)
  ) {
    candidateVariants = rankedVariants.filter(
      (variant) => variant.edition_key === effectiveEditionKey,
    );
  } else {
    const firstRankedVariant = rankedVariants[0];
    if (!firstRankedVariant) {
      return selectDefaultVersion(versions, userData, qualityPreference, preferredEditionKey);
    }
    const preferredRank = playbackVariantEditionPreference(firstRankedVariant);
    candidateVariants = rankedVariants.filter(
      (variant) => playbackVariantEditionPreference(variant) === preferredRank,
    );
  }

  // The persisted pick wins even when an edition filter above moved the
  // candidate set: choosing a different edition silently would be a server
  // substitution in the page's own selection logic.
  if (userData?.last_file_id != null) {
    const matching = versions.find((version) => version.file_id === userData.last_file_id);
    if (matching) {
      return matching;
    }
  }

  for (const variant of candidateVariants) {
    const firstPart = [...(variant.parts ?? [])].sort((a, b) => a.part_index - b.part_index)[0];
    if (!firstPart) {
      continue;
    }
    if (firstPart.default_file_id != null) {
      const matching = versions.find((version) => version.file_id === firstPart.default_file_id);
      if (matching) {
        return matching;
      }
    }
    const fallback = selectDefaultVersion(
      firstPart.versions ?? versions,
      userData,
      qualityPreference,
      variant.edition_key ?? effectiveEditionKey,
    );
    if (fallback) {
      return fallback;
    }
  }

  return selectDefaultVersion(versions, userData, qualityPreference, preferredEditionKey);
}
