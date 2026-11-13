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
  return RESOLUTION_RANK[res.trim().toLowerCase()] ?? -1;
}

function audioScore(codec: string): number {
  const lower = codec.toLowerCase();
  for (const [key, score] of Object.entries(AUDIO_RANK)) {
    if (lower.includes(key)) return score;
  }
  return -1;
}

/**
 * Edition preference for the no-history comparison across cuts: 0 for the
 * unnamed or standard cut, 1 for a theatrical cut, 2 for anything else.
 */
function editionPreference(key: string, raw?: string): number {
  if (!key) return 0;
  const values = [key, raw]
    .map((value) => value?.trim().toLowerCase())
    .filter((value): value is string => Boolean(value));
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
  if (values.some((value) => value.includes("theatrical"))) return 1;
  return 2;
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

/** The server's version order (sortFileVersions): rank, then HDR, then larger file. */
function compareServerOrder(a: FileVersion, b: FileVersion): number {
  return (
    resolutionScore(b.resolution) - resolutionScore(a.resolution) ||
    Number(b.hdr) - Number(a.hdr) ||
    (b.file_size ?? 0) - (a.file_size ?? 0)
  );
}

/**
 * The file Version Auto plays among one edition's first-part files. An
 * unmeasured file is a last resort, never evidence that it fits the cap.
 */
export function selectDefaultVersion(
  versions: FileVersion[],
  qualityPreference?: string | null,
): FileVersion | null {
  const ordered = [...versions].sort(compareServerOrder);
  const known = ordered.filter((v) => resolutionScore(v.resolution) >= 0);
  if (known.length === 0) return ordered[0] ?? null;
  const cap = resolutionScore(qualityPreference ?? "auto");
  if (cap < 0) return known[0] ?? null;
  const fits = known.find((v) => resolutionScore(v.resolution) <= cap);
  if (fits) return fits;
  // Nothing fits: the closest file above the cap, the lowest rank in server order.
  const lowest = Math.min(...known.map((v) => resolutionScore(v.resolution)));
  return known.find((v) => resolutionScore(v.resolution) === lowest) ?? null;
}

export interface PlaybackEdition {
  key: string;
  raw?: string;
  versions: FileVersion[];
  allVersions: FileVersion[];
}

/**
 * One cut can have several presentations. Only their first parts start
 * playback. Editions come in preference order, then the server's order.
 */
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
    for (const variant of variants) {
      const parts = [...variant.parts].sort((a, b) => a.part_index - b.part_index);
      if (!parts[0]?.versions.length) continue;
      add(
        variant.edition_key?.trim() ?? "",
        variant.edition_raw,
        parts[0].versions,
        parts.flatMap((p) => p.versions),
      );
    }
  } else {
    for (const version of versions) {
      add(version.edition_key?.trim() ?? "", version.edition_raw, [version], [version]);
    }
  }
  return [...editions.values()]
    .map((edition, index) => ({ edition, index }))
    .sort(
      (a, b) =>
        editionPreference(a.edition.key, a.edition.raw) -
          editionPreference(b.edition.key, b.edition.raw) || a.index - b.index,
    )
    .map(({ edition }) => edition);
}

/**
 * With no edition picked or remembered, each cut's Auto file competes: a file
 * within the cap (or any file without one) beats one above it, which beats an
 * unmeasured one. Within the cap the higher resolution wins, above it the
 * lower; then HDR, then edition preference and server order. File size is not
 * compared across cuts.
 */
function bestEditionAuto(
  editions: PlaybackEdition[],
  qualityPreference?: string | null,
): FileVersion | null {
  const cap = resolutionScore(qualityPreference ?? "auto");
  const ranked = editions.flatMap((edition) => {
    const file = selectDefaultVersion(edition.versions, qualityPreference);
    if (!file) return [];
    const rank = resolutionScore(file.resolution);
    const fitClass = rank < 0 ? 2 : cap < 0 || rank <= cap ? 0 : 1;
    return [{ file, rank, fitClass }];
  });
  // editions already come in preference, then server, order; sort is stable.
  ranked.sort(
    (a, b) =>
      a.fitClass - b.fitClass ||
      (a.fitClass === 0 ? b.rank - a.rank : a.rank - b.rank) ||
      Number(b.file.hdr) - Number(a.file.hdr),
  );
  return ranked[0]?.file ?? null;
}

/**
 * The file the detail page shows and plays when no version is pinned: Auto
 * within the current edition (picked, remembered, or the edition of the last
 * played file), or the best cut when there is no history. While the item has
 * a resume point, a last played file that is a later part of a presentation in
 * the current edition is resumed as is; otherwise playback starts at part 1.
 */
export function selectDefaultPlaybackVariantVersion(
  versions: FileVersion[],
  playbackVariants: PlaybackVariant[] | undefined,
  userData?: LeafItemUserData,
  qualityPreference?: string | null,
  preferredEditionKey?: string | null,
): FileVersion | null {
  const editions = playbackEditions(versions, playbackVariants);
  const lastFileId = userData?.last_file_id;
  const edition =
    editions.find((e) => e.key === preferredEditionKey?.trim()) ??
    editions.find((e) => e.key === userData?.last_edition_key?.trim()) ??
    editions.find((e) => e.allVersions.some((v) => v.file_id === lastFileId));
  if (!edition) return bestEditionAuto(editions, qualityPreference);
  const resumable = (userData?.position_seconds ?? 0) > 0 && !userData?.played;
  const laterPart = resumable
    ? edition.allVersions.find((v) => v.file_id === lastFileId && !edition.versions.includes(v))
    : undefined;
  return laterPart ?? selectDefaultVersion(edition.versions, qualityPreference);
}

/** The parts of the presentation that holds `file`, in part order. */
function presentationParts(file: FileVersion, variants?: PlaybackVariant[]) {
  const variant = variants?.find((v) =>
    v.parts.some((p) => p.versions.some((candidate) => candidate.file_id === file.file_id)),
  );
  return variant ? [...variant.parts].sort((a, b) => a.part_index - b.part_index) : [];
}

/** Start Over plays the selected presentation from its first part. */
export function selectedStartVersion(
  selected: FileVersion | null,
  variants?: PlaybackVariant[],
  qualityPreference?: string | null,
): FileVersion | null {
  if (!selected) return null;
  const first = presentationParts(selected, variants)[0];
  if (!first || first.versions.some((v) => v.file_id === selected.file_id)) return selected;
  return selectDefaultVersion(first.versions, qualityPreference) ?? selected;
}

/** Download the selected presentation: the resolved file and Auto for its other parts. */
export function selectedDownloadVersions(
  selected: FileVersion | null,
  variants?: PlaybackVariant[],
  qualityPreference?: string | null,
): FileVersion[] {
  if (!selected) return [];
  const parts = presentationParts(selected, variants);
  if (parts.length === 0) return [selected];
  return parts.flatMap((part) => {
    const file = part.versions.some((v) => v.file_id === selected.file_id)
      ? selected
      : selectDefaultVersion(part.versions, qualityPreference);
    return file ? [file] : [];
  });
}
