import type { FileVersion, LeafItemUserData, PlaybackVariant } from "@/api/types";
import { audioTrackAtmosEvidence, mapAudioLabel } from "@/lib/mediaFormat";
import { versionAudioAtmosEvidence } from "./versionFormatUtils";

const RESOLUTION_RANK: Record<string, number> = {
  "8k": 5,
  "4320p": 5,
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

function audioScore(codec: string): number {
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

/** Rank actual files, keeping the quality cap in effect through tie breaking. */
export function selectDefaultVersion(
  versions: FileVersion[],
  userData?: LeafItemUserData,
  qualityPreference?: string | null,
  preferredEditionKey?: string | null,
): FileVersion | null {
  const editionKey =
    preferredEditionKey ??
    userData?.last_edition_key ??
    versions.find((version) => version.file_id === userData?.last_file_id)?.edition_key;
  let candidates =
    editionKey != null && versions.some((v) => (v.edition_key ?? "") === editionKey)
      ? versions.filter((v) => (v.edition_key ?? "") === editionKey)
      : versions;
  // An unmeasured file is a fallback, not evidence that it fits a quality cap.
  const knownResolution = candidates.filter((v) => resolutionScore(v.resolution) >= 0);
  if (knownResolution.length > 0) candidates = knownResolution;
  const cap = resolutionScore(qualityPreference ?? "auto");
  let aboveCap = false;
  if (cap >= 0) {
    const withinCap = candidates.filter((v) => {
      const score = resolutionScore(v.resolution);
      return score >= 0 && score <= cap;
    });
    aboveCap = withinCap.length === 0;
    if (!aboveCap) candidates = withinCap;
  }
  return (
    [...candidates].sort(
      (a, b) =>
        (aboveCap ? 1 : -1) * (resolutionScore(a.resolution) - resolutionScore(b.resolution)) ||
        Number(b.hdr) - Number(a.hdr) ||
        audioScore(b.codec_audio) - audioScore(a.codec_audio),
    )[0] ?? null
  );
}

export interface PlaybackEdition {
  key: string;
  raw?: string;
  versions: FileVersion[];
  allVersions: FileVersion[];
}

/** One cut can have several presentations. Only their first parts start playback. */
export function playbackEditions(
  versions: FileVersion[],
  variants?: PlaybackVariant[],
): PlaybackEdition[] {
  const editions = new Map<string, PlaybackEdition>();
  const add = (key: string, raw: string | undefined, first: FileVersion[], all: FileVersion[]) => {
    const edition = editions.get(key) ?? { key, raw, versions: [], allVersions: [] };
    edition.versions.push(...first);
    edition.allVersions.push(...all);
    editions.set(key, edition);
  };
  if (variants?.length) {
    for (const variant of sortPlaybackVariantsByEditionPreference(variants)) {
      const parts = [...variant.parts].sort((a, b) => a.part_index - b.part_index);
      if (!parts[0]?.versions.length) continue;
      add(
        variant.edition_key ?? "",
        variant.edition_raw,
        parts[0].versions,
        parts.flatMap((p) => p.versions),
      );
    }
  } else {
    for (const version of versions) {
      add(version.edition_key ?? "", version.edition_raw, [version], [version]);
    }
  }
  return [...editions.values()];
}

export function selectDefaultPlaybackVariantVersion(
  versions: FileVersion[],
  playbackVariants: PlaybackVariant[] | undefined,
  userData?: LeafItemUserData,
  qualityPreference?: string | null,
  preferredEditionKey?: string | null,
): FileVersion | null {
  const editions = playbackEditions(versions, playbackVariants);
  const edition =
    editions.find((e) => e.key === preferredEditionKey) ??
    editions.find((e) => e.key === userData?.last_edition_key) ??
    editions.find((e) => e.allVersions.some((v) => v.file_id === userData?.last_file_id));
  return selectDefaultVersion(
    edition?.versions ?? editions.flatMap((e) => e.versions),
    undefined,
    qualityPreference,
  );
}

/** Download the resolved file; multipart presentations also expose their later parts. */
export function selectedDownloadVersions(
  selected: FileVersion | null,
  variants?: PlaybackVariant[],
  qualityPreference?: string | null,
): FileVersion[] {
  if (!selected) return [];
  const variant = variants?.find((v) =>
    v.parts.some((p) => p.versions.some((file) => file.file_id === selected.file_id)),
  );
  const remainingParts =
    variant?.parts.filter((p) => !p.versions.some((file) => file.file_id === selected.file_id)) ??
    [];
  return [
    selected,
    ...remainingParts.flatMap((part) => {
      const file = selectDefaultVersion(part.versions, undefined, qualityPreference);
      return file ? [file] : [];
    }),
  ];
}
