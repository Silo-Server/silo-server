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
  external: 0,
  downloaded: 1,
  embedded: 2,
};

/**
 * Auto-select priority within the same language rank: lower is better. Within the same source
 * tier, text tracks beat bitmap (PGS) tracks — bitmap is heavier to render
 * and can't be styled — while a bitmap track still wins when it's the only
 * match for the language.
 *
 * `preferEmbeddedSubtitles` reverses the source tiers for a profile that chose
 * `playback.prefer_embedded_subtitles`: an external file is cut for one release
 * and can play out of sync, while the track inside the file cannot. The middle
 * tier (downloaded subtitles) keeps its place, and the reversal sits inside the
 * source tier only, so language rank is unaffected.
 *
 * It also makes a full track outrank a signs-only one, which the source tier is
 * otherwise blind to: turning the preference on must not make a forced embedded
 * track the default while the file still offers a full external one.
 * FULL_DIALOGUE_WEIGHT exceeds every value the tiers below it can produce
 * together, which keeps the comparison lexicographic.
 */
const FULL_DIALOGUE_WEIGHT = 6;
const SOURCE_TIER_CEILING = 2;

function trackPriority(track: PlayerSubtitleInfo, preferEmbedded: boolean): number {
  const tier = SOURCE_PRIORITY[track.source ?? "embedded"] ?? SOURCE_TIER_CEILING;
  const source = preferEmbedded ? SOURCE_TIER_CEILING - tier : tier;
  const belowSource = source * SOURCE_TIER_CEILING + (isBitmapCodec(track.codec) ? 1 : 0);
  if (!preferEmbedded) return belowSource;
  return (track.forced ? 1 : 0) * FULL_DIALOGUE_WEIGHT + belowSource;
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

/** Sort subtitle tracks: external first, then downloaded, then embedded. */
export function sortSubtitlesBySource(tracks: PlayerSubtitleInfo[]): PlayerSubtitleInfo[] {
  return [...tracks].sort((a, b) => {
    const pa = SOURCE_PRIORITY[a.source ?? "embedded"] ?? 2;
    const pb = SOURCE_PRIORITY[b.source ?? "embedded"] ?? 2;
    return pa - pb;
  });
}

/**
 * Find the best subtitle track index for a given language: exact tag, then
 * bare language, then another variant of the same language. Within a language
 * rank, prefer external > downloaded > embedded (embedded first when the
 * profile prefers embedded subtitles), then text over bitmap.
 * Returns the track's backend index (track.index) or -1 if no match.
 */
export function findPreferredSubtitleIndex(
  tracks: PlayerSubtitleInfo[],
  language: string,
  preferEmbedded = false,
): number {
  let bestIdx = -1;
  let bestLanguageRank = 3;
  let bestPriority = Infinity;

  for (const track of tracks) {
    if (!track) continue;
    const languageRank = languageMatchRank(track.language, language);
    if (languageRank < 0) continue;
    const priority = trackPriority(track, preferEmbedded);
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
  preferEmbedded: boolean,
): number {
  let bestTrack: PlayerSubtitleInfo | null = null;
  let bestScore = -1;
  let bestLanguageRank = 3;
  let bestPriority = Infinity;

  for (const track of tracks) {
    if (!track) continue;
    const languageRank = languageMatchRank(track.language, language);
    if (languageRank < 0) continue;
    const priority = trackPriority(track, preferEmbedded);
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
  /** The profile's `playback.prefer_embedded_subtitles` choice. */
  preferEmbeddedSubtitles?: boolean;
}

function findForcedSubtitleIndex(
  tracks: PlayerSubtitleInfo[],
  language: string | null | undefined,
  preferEmbedded: boolean,
): number | null {
  if (!language) return null;
  const match = findPreferredSubtitleIndex(
    tracks.filter((track) => track.forced),
    language,
    preferEmbedded,
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
    preferEmbeddedSubtitles = false,
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
      return showForcedSubtitles
        ? findForcedSubtitleIndex(tracks, effectiveAudioLang, preferEmbeddedSubtitles)
        : null;

    case "always": {
      const exactMatch = findExactSubtitleSignatureMatch(tracks, signature);
      if (exactMatch !== null) return exactMatch;
      if (!preferredLanguage) return null;
      const match = findPreferredSubtitleIndexWithSignature(
        tracks,
        preferredLanguage,
        signature,
        preferEmbeddedSubtitles,
      );
      return match >= 0 ? match : null;
    }

    case "auto": {
      if (preferredLanguage === "") return null;
      if (effectiveProfileLang && sameLanguageCode(effectiveAudioLang, effectiveProfileLang)) {
        return showForcedSubtitles
          ? findForcedSubtitleIndex(tracks, effectiveAudioLang, preferEmbeddedSubtitles)
          : null;
      }
      const lang = preferredSubtitleLang ?? effectiveProfileLang;
      if (!lang) {
        return showForcedSubtitles
          ? findForcedSubtitleIndex(tracks, effectiveAudioLang, preferEmbeddedSubtitles)
          : null;
      }
      const match = findPreferredSubtitleIndexWithSignature(
        tracks,
        lang,
        signature,
        preferEmbeddedSubtitles,
      );
      return match >= 0 ? match : null;
    }

    default:
      return null;
  }
}
