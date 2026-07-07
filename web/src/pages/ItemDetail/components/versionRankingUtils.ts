import type { FileVersion, LeafItemUserData, PlaybackVariant } from "@/api/types";
import { mapAudioLabel } from "@/lib/mediaFormat";

export const RESOLUTION_RANK: Record<string, number> = {
  "4k": 4,
  "2160p": 4,
  "1440p": 3,
  "1080p": 2,
  "720p": 1,
  "480p": 0,
};

export const AUDIO_RANK: Record<string, number> = {
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
  let bestAudioScore = -1;

  for (const v of candidates) {
    const rs = resolutionScore(v.resolution);
    if (rs > bestResScore) {
      bestResScore = rs;
      bestRes = v.resolution;
    }
    if (v.hdr) hdr = true;

    const as_ = audioScore(v.codec_audio);
    if (as_ > bestAudioScore) {
      bestAudioScore = as_;
      bestAudioCodec = v.codec_audio;
    }

    // Also check individual audio tracks for higher-quality codecs
    if (v.audio_tracks) {
      for (const t of v.audio_tracks) {
        if (t.codec) {
          const ts = audioScore(t.codec);
          if (ts > bestAudioScore) {
            bestAudioScore = ts;
            bestAudioCodec = t.codec;
          }
        }
      }
    }
  }

  return {
    resolution: bestRes,
    hdr,
    audioLabel: bestAudioCodec ? mapAudioLabel(bestAudioCodec) : "",
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

  // Prefer the version they last watched.
  if (userData?.last_file_id != null) {
    const matching = versions.find((v) => v.file_id === userData.last_file_id);
    if (matching && (!effectiveEditionKey || matching.edition_key === effectiveEditionKey)) {
      return matching;
    }
  }

  const candidates =
    effectiveEditionKey && versions.some((v) => v.edition_key === effectiveEditionKey)
      ? versions.filter((v) => v.edition_key === effectiveEditionKey)
      : versions;

  if (candidates.length === 1) return candidates[0] ?? null;

  // Use quality-preference ranking to find the best match.
  const preferred = pickBestAttributes(candidates, qualityPreference);
  if (preferred) {
    const matching = candidates.find(
      (v) =>
        v.resolution === preferred.resolution &&
        v.hdr === preferred.hdr &&
        (preferred.audioLabel === "" ||
          mapAudioLabel(v.codec_audio).toLowerCase() === preferred.audioLabel.toLowerCase()),
    );
    if (matching) return matching;
  }

  // Fall back to highest resolution.
  const sorted = [...candidates].sort(
    (a, b) => resolutionScore(b.resolution) - resolutionScore(a.resolution),
  );
  return sorted[0] ?? null;
}

export function selectDefaultPlaybackVariantVersion(
  versions: FileVersion[],
  playbackVariants: PlaybackVariant[] | undefined,
  userData?: LeafItemUserData,
  qualityPreference?: string | null,
  preferredEditionKey?: string | null,
): FileVersion | null {
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

  if (userData?.last_file_id != null) {
    for (const variant of candidateVariants) {
      for (const part of variant.parts ?? []) {
        const matching = part.versions.find((version) => version.file_id === userData.last_file_id);
        if (matching) {
          return matching;
        }
      }
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
