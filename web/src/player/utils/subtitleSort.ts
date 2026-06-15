import type { PlayerSubtitleInfo, PlayerSubtitleTrackSignature, SubtitleMode } from "../types";
import { normalizeLanguageCode } from "./languageNames";
import { isBitmapCodec } from "./subtitleCodecs";

const ORIGINAL_LANGUAGE_SENTINEL = "original";

const SOURCE_PRIORITY: Record<string, number> = {
  external: 0,
  downloaded: 1,
  embedded: 2,
};

/**
 * Auto-select priority for a track: lower is better. Within the same source
 * tier, text tracks beat bitmap (PGS) tracks — bitmap is heavier to render
 * and can't be styled — while a bitmap track still wins when it's the only
 * match for the language.
 */
function trackPriority(track: PlayerSubtitleInfo): number {
  const source = SOURCE_PRIORITY[track.source ?? "embedded"] ?? 2;
  return source * 2 + (isBitmapCodec(track.codec) ? 1 : 0);
}

function normalize(value: string | undefined | null): string {
  return (value ?? "").trim().toLowerCase();
}

function normalizeConcreteLanguage(value: string | undefined | null): string | null {
  const normalized = normalize(value);
  if (!normalized || normalized === ORIGINAL_LANGUAGE_SENTINEL) {
    return null;
  }
  return normalized;
}

function sameLanguageCode(a: string | undefined | null, b: string | undefined | null): boolean {
  const left = normalizeConcreteLanguage(a);
  const right = normalizeConcreteLanguage(b);
  if (!left || !right) return false;
  return normalizeLanguageCode(left) === normalizeLanguageCode(right);
}

function sameLanguage(track: PlayerSubtitleInfo, language: string): boolean {
  return sameLanguageCode(track.language, language);
}

function subtitleTrackMatchesSignature(
  track: PlayerSubtitleInfo,
  signature: PlayerSubtitleTrackSignature | null,
): boolean {
  if (!signature) return false;
  return (
    normalize(track.source) === normalize(signature.source) &&
    normalize(track.language) === normalize(signature.language) &&
    normalize(track.codec) === normalize(signature.codec) &&
    (normalize(signature.label) === "" || normalize(track.label) === normalize(signature.label)) &&
    Boolean(track.forced) === Boolean(signature.forced) &&
    Boolean(track.hearing_impaired) === Boolean(signature.hearing_impaired)
  );
}

function findExactSubtitleSignatureMatch(
  tracks: PlayerSubtitleInfo[],
  signature: PlayerSubtitleTrackSignature | null,
): number | null {
  if (!signature) return null;
  const match = tracks.find((track) => subtitleTrackMatchesSignature(track, signature));
  return match?.index ?? null;
}

function scoreSignatureFallback(
  track: PlayerSubtitleInfo,
  signature: PlayerSubtitleTrackSignature | null,
): number {
  if (!signature) return 0;
  let score = 0;
  if (normalize(track.source) === normalize(signature.source)) score += 4;
  if (Boolean(track.forced) === Boolean(signature.forced)) score += 2;
  if (Boolean(track.hearing_impaired) === Boolean(signature.hearing_impaired)) score += 2;
  if (normalize(track.codec) === normalize(signature.codec)) score += 1;
  if (normalize(track.label) === normalize(signature.label)) score += 1;
  return score;
}

/** Sort subtitle tracks: external first, then downloaded, then embedded. */
export function sortSubtitlesBySource(tracks: PlayerSubtitleInfo[]): PlayerSubtitleInfo[] {
  return [...tracks].sort((a, b) => {
    const pa = SOURCE_PRIORITY[a.source ?? "embedded"] ?? 2;
    const pb = SOURCE_PRIORITY[b.source ?? "embedded"] ?? 2;
    return pa - pb;
  });
}

/**
 * Find the best subtitle track index for a given language,
 * preferring external > downloaded > embedded.
 * Returns the track's backend index (track.index) or -1 if no match.
 */
export function findPreferredSubtitleIndex(tracks: PlayerSubtitleInfo[], language: string): number {
  let bestIdx = -1;
  let bestPriority = Infinity;

  for (const track of tracks) {
    if (!track || !sameLanguage(track, language)) continue;
    const priority = trackPriority(track);
    if (priority < bestPriority) {
      bestPriority = priority;
      bestIdx = track.index;
    }
  }

  return bestIdx;
}

function findPreferredSubtitleIndexWithSignature(
  tracks: PlayerSubtitleInfo[],
  language: string,
  signature: PlayerSubtitleTrackSignature | null,
): number {
  let bestTrack: PlayerSubtitleInfo | null = null;
  let bestScore = -1;
  let bestPriority = Infinity;

  for (const track of tracks) {
    if (!track || !sameLanguage(track, language)) continue;
    const priority = trackPriority(track);
    const score = scoreSignatureFallback(track, signature);
    if (
      bestTrack === null ||
      score > bestScore ||
      (score === bestScore && priority < bestPriority)
    ) {
      bestTrack = track;
      bestScore = score;
      bestPriority = priority;
    }
  }

  return bestTrack?.index ?? -1;
}

export interface SubtitleAutoSelectOptions {
  mode: SubtitleMode;
  tracks: PlayerSubtitleInfo[];
  preferredLanguage: string | null;
  preferredTrackSignature?: PlayerSubtitleTrackSignature | null;
  audioLanguage: string | null;
  profileLanguage: string | null;
  showForcedSubtitles: boolean;
}

function findForcedSubtitleIndex(
  tracks: PlayerSubtitleInfo[],
  language: string | null | undefined,
): number | null {
  if (!language) return null;
  const match = findPreferredSubtitleIndex(
    tracks.filter((track) => track.forced),
    language,
  );
  return match >= 0 ? match : null;
}

/**
 * Determines which subtitle track to auto-select on playback start.
 * Returns the track's backend index, or null if no track should be selected.
 */
export function resolveSubtitleAutoSelect(options: SubtitleAutoSelectOptions): number | null {
  const {
    mode,
    tracks,
    preferredLanguage,
    preferredTrackSignature,
    audioLanguage,
    profileLanguage,
    showForcedSubtitles,
  } = options;
  const signature = preferredTrackSignature ?? null;

  if (tracks.length === 0) return null;
  const preferredSubtitleLang = normalizeConcreteLanguage(preferredLanguage);
  const normalizedProfileLanguage = normalize(profileLanguage);
  const effectiveProfileLang =
    normalizeConcreteLanguage(profileLanguage) ??
    (normalizedProfileLanguage === ORIGINAL_LANGUAGE_SENTINEL
      ? preferredSubtitleLang
      : normalizedProfileLanguage === ""
        ? "en"
        : null);
  const effectiveAudioLang = normalizeConcreteLanguage(audioLanguage) ?? effectiveProfileLang;

  switch (mode) {
    case "off":
      return showForcedSubtitles ? findForcedSubtitleIndex(tracks, effectiveAudioLang) : null;

    case "always": {
      const exactMatch = findExactSubtitleSignatureMatch(tracks, signature);
      if (exactMatch !== null) return exactMatch;
      if (!preferredLanguage) return null;
      const match = findPreferredSubtitleIndexWithSignature(tracks, preferredLanguage, signature);
      return match >= 0 ? match : null;
    }

    case "auto": {
      if (preferredLanguage === "") return null;
      if (effectiveProfileLang && sameLanguageCode(effectiveAudioLang, effectiveProfileLang)) {
        return showForcedSubtitles ? findForcedSubtitleIndex(tracks, effectiveAudioLang) : null;
      }
      const lang = preferredSubtitleLang ?? effectiveProfileLang;
      if (!lang) {
        return showForcedSubtitles ? findForcedSubtitleIndex(tracks, effectiveAudioLang) : null;
      }
      const match = findPreferredSubtitleIndexWithSignature(tracks, lang, signature);
      return match >= 0 ? match : null;
    }

    default:
      return null;
  }
}
