import type { PlayerSubtitleInfo, PlayerSubtitleTrackSignature, SubtitleMode } from "../types";
import { canonicalLanguageTag, normalizeLanguageCode } from "@/lib/languageTags";
import { isBitmapCodec } from "./subtitleCodecs";

const ORIGINAL_LANGUAGE_SENTINEL = "original";
// playback.audio_language stores the original-language choice as this
// private-use tag (the settings contract only holds language tags).
const ORIGINAL_LANGUAGE_TAG = "x-silo-original";

function isOriginalLanguagePreference(normalized: string): boolean {
  return normalized === ORIGINAL_LANGUAGE_SENTINEL || normalized === ORIGINAL_LANGUAGE_TAG;
}

const SOURCE_PRIORITY: Record<string, number> = {
  embedded: 0,
  external: 1,
  downloaded: 2,
};

function sourcePriority(track: PlayerSubtitleInfo): number {
  return SOURCE_PRIORITY[track.source ?? "embedded"] ?? 0;
}

/**
 * Whether showing the track forces a burn-in transcode. The web player renders
 * no bitmap subtitles (`client-context-v3.ts` declares no bitmap support), so
 * every PGS/DVD/DVB track is burned in, even one the server could deliver as a
 * sidecar. The server's `burn_in_only` flag covers the rest.
 */
function needsBurnIn(track: PlayerSubtitleInfo): boolean {
  return isBitmapCodec(track.codec) || track.burn_in_only === true;
}

/**
 * Auto-select priority within the same language rank: lower is better. Each
 * tier only breaks ties in the one before it:
 * 1. a track the player renders itself beats one that forces a burn-in;
 * 2. full dialogue beats forced, and plain beats SDH, so a file's own forced
 *    or SDH track never displaces the full track the viewer asked for;
 * 3. embedded beats external beats downloaded.
 * A burn-in track still wins when it's the only match for the language.
 */
function trackPriority(track: PlayerSubtitleInfo): number {
  // Source (0-2) stays below 4, so each flag weight outranks every lower
  // tier combined.
  return (
    (needsBurnIn(track) ? 16 : 0) +
    (track.forced ? 8 : 0) +
    (track.hearing_impaired ? 4 : 0) +
    sourcePriority(track)
  );
}

function normalize(value: string | undefined | null): string {
  return (value ?? "").trim().toLowerCase();
}

function normalizeConcreteLanguage(value: string | undefined | null): string | null {
  const normalized = normalize(value);
  if (!normalized || isOriginalLanguagePreference(normalized)) {
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

function languageMatchRank(candidate: string | undefined | null, preferred: string): number {
  const candidateTag = canonicalLanguageTag(candidate ?? "");
  const preferredTag = canonicalLanguageTag(preferred);
  if (!candidateTag || !preferredTag) return -1;
  if (candidateTag === preferredTag) return 0;
  if (normalizeLanguageCode(candidateTag) !== normalizeLanguageCode(preferredTag)) return -1;
  return candidateTag.includes("-") ? 2 : 1;
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

/** Sort subtitle tracks: embedded first, then external, then downloaded. */
export function sortSubtitlesBySource(tracks: PlayerSubtitleInfo[]): PlayerSubtitleInfo[] {
  return [...tracks].sort((a, b) => sourcePriority(a) - sourcePriority(b));
}

/**
 * Find the best subtitle track index for a given language: exact tag, then
 * bare language, then another variant of the same language. Within a language
 * rank, order candidates by trackPriority.
 * Returns the track's backend index (track.index) or -1 if no match.
 */
export function findPreferredSubtitleIndex(tracks: PlayerSubtitleInfo[], language: string): number {
  let bestIdx = -1;
  let bestLanguageRank = 3;
  let bestPriority = Infinity;

  for (const track of tracks) {
    if (!track) continue;
    const languageRank = languageMatchRank(track.language, language);
    if (languageRank < 0) continue;
    const priority = trackPriority(track);
    if (
      languageRank < bestLanguageRank ||
      (languageRank === bestLanguageRank && priority < bestPriority)
    ) {
      bestLanguageRank = languageRank;
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
  let bestLanguageRank = 3;
  let bestPriority = Infinity;

  for (const track of tracks) {
    if (!track) continue;
    const languageRank = languageMatchRank(track.language, language);
    if (languageRank < 0) continue;
    const priority = trackPriority(track);
    const score = scoreSignatureFallback(track, signature);
    if (
      bestTrack === null ||
      languageRank < bestLanguageRank ||
      (languageRank === bestLanguageRank &&
        (score > bestScore || (score === bestScore && priority < bestPriority)))
    ) {
      bestTrack = track;
      bestScore = score;
      bestLanguageRank = languageRank;
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
    (isOriginalLanguagePreference(normalizedProfileLanguage)
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
