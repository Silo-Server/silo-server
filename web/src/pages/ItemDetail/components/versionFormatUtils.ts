import type {
  FileVersion,
  VersionAudioTrack,
  VersionSubtitleTrack,
  VersionVideoTrack,
} from "@/api/types";
import { englishLanguageName, getLanguageName } from "@/lib/languageNames";
import {
  audioTrackAtmosEvidence,
  formatBitrate,
  formatChannels,
  formatSampleRate,
} from "@/lib/mediaFormat";

/**
 * Atmos evidence for the audio stream a version's codec_audio describes. The
 * scanner takes codec_audio from the first audio stream, so the first track's
 * profile and titles apply when its codec agrees.
 */
export function versionAudioAtmosEvidence(version: FileVersion): string | undefined {
  const first = version.audio_tracks?.[0];
  if (!first?.codec || !version.codec_audio) return undefined;
  return first.codec.toLowerCase() === version.codec_audio.toLowerCase()
    ? audioTrackAtmosEvidence(first)
    : undefined;
}

export function formatPageCount(pages?: number): string {
  if (!pages || pages <= 0) return "";
  return `${pages.toLocaleString()} ${pages === 1 ? "page" : "pages"}`;
}

export function formatAddedAt(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat("en-US", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

export function formatLanguageName(language?: string): string {
  if (!language) return "";

  const trimmed = language.trim();
  const normalized = trimmed.toLowerCase();
  if (!normalized) return "";

  const standardizedName = englishLanguageName(trimmed);
  if (standardizedName) return standardizedName;

  if (normalized.length > 3) {
    return trimmed
      .split(/[\s_-]+/)
      .filter(Boolean)
      .map((part) => part[0]?.toUpperCase() + part.slice(1).toLowerCase())
      .join(" ");
  }
  return getLanguageName(trimmed);
}

const SOURCE_HINT_PATTERNS: Array<{ pattern: RegExp; canonical: string }> = [
  { pattern: /\bremux\b/i, canonical: "Remux" },
  { pattern: /\bweb-dl\b/i, canonical: "WEB-DL" },
  { pattern: /\bwebrip\b/i, canonical: "WEBRip" },
  { pattern: /\bbluray\b/i, canonical: "BluRay" },
  { pattern: /\bbdrip\b/i, canonical: "BDRip" },
  { pattern: /\bhdtv\b/i, canonical: "HDTV" },
  { pattern: /\bdvdrip\b/i, canonical: "DVDRip" },
];

export function extractSourceHint(fileName: string): string | null {
  for (const { pattern, canonical } of SOURCE_HINT_PATTERNS) {
    if (pattern.test(fileName)) return canonical;
  }
  return null;
}

// An episode token such as S01E01, S01E01E02 or S01E01-E02.
const EPISODE_TOKEN = /\bS\d{1,4}E\d{1,4}(?:[-_.]?E\d{1,4})*\b/i;

/**
 * The part of a version's file name that tells it apart from the other
 * versions: the base name without its extension and, for an episode, without
 * the show and episode prefix up to the SxxExx token. Falls back to the whole
 * base name when no token is found or nothing follows it.
 */
export function versionFileLabel(fileName: string | undefined): string {
  const name = fileName?.trim() ?? "";
  if (!name) return "";

  const dot = name.lastIndexOf(".");
  const stem = dot > 0 ? name.slice(0, dot) : name;

  const token = EPISODE_TOKEN.exec(stem);
  if (token) {
    const rest = stem.slice(token.index + token[0].length).replace(/^[\s._-]+/, "");
    if (rest) return rest;
  }
  return stem;
}

export function metadataLine(parts: Array<string | undefined | false>): string {
  return parts.filter(Boolean).join(" \u00B7 ");
}

export function videoTitle(track: VersionVideoTrack): string {
  return (
    track.title ||
    [track.width && track.height ? `${track.width}x${track.height}` : "", track.codec]
      .filter(Boolean)
      .join(" ") ||
    "Video"
  );
}

export function audioTitle(track: VersionAudioTrack): string {
  return (
    track.title ||
    track.embedded_title ||
    [track.language, track.codec, formatChannels(track.channels)].filter(Boolean).join(" ") ||
    "Audio"
  );
}

export function subtitleTitle(track: VersionSubtitleTrack): string {
  const language = formatLanguageName(track.language);
  const title = track.title || track.embedded_title || track.codec || "Subtitle";

  const languageLower = language.toLowerCase();
  const titleLower = title.toLowerCase();

  if (language && title && !titleLower.includes(languageLower)) {
    return `${language} - ${title}`;
  }

  return title || language;
}

export function compactVideoMeta(track: VersionVideoTrack): string {
  return metadataLine([
    track.profile,
    track.aspect_ratio,
    track.frame_rate ? `${track.frame_rate} fps` : "",
    formatBitrate(track.bitrate),
    track.bit_depth ? `${track.bit_depth}-bit` : "",
    track.video_range,
    track.dolby_vision,
    track.color_space,
    track.interlaced ? "Interlaced" : "",
  ]);
}

export function compactAudioMeta(track: VersionAudioTrack): string {
  return metadataLine([
    track.layout,
    formatBitrate(track.bitrate),
    formatSampleRate(track.sample_rate),
    track.bit_depth ? `${track.bit_depth}-bit` : "",
  ]);
}

export function compactSubtitleMeta(track: VersionSubtitleTrack): string {
  return metadataLine([
    track.external ? "External" : "Embedded",
    track.forced ? "Forced" : "",
    track.hearing_impaired ? "HI" : "",
    track.default ? "Default" : "",
    track.resolution,
  ]);
}
