import type { VersionAudioTrack, VersionSubtitleTrack, VersionVideoTrack } from "@/api/types";
import { formatBitrate, formatChannels, formatSampleRate } from "@/lib/mediaFormat";

export const LANGUAGE_NAMES: Record<string, string> = {
  cze: "Czech",
  ces: "Czech",
  deu: "German",
  eng: "English",
  fra: "French",
  fre: "French",
  ita: "Italian",
  jpn: "Japanese",
  por: "Portuguese",
  spa: "Spanish",
};

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
  if (LANGUAGE_NAMES[normalized]) return LANGUAGE_NAMES[normalized];

  if (normalized.length > 3) {
    return trimmed
      .split(/[\s_-]+/)
      .filter(Boolean)
      .map((part) => part[0]?.toUpperCase() + part.slice(1).toLowerCase())
      .join(" ");
  }

  if (typeof Intl !== "undefined" && "DisplayNames" in Intl) {
    try {
      const displayNames = new Intl.DisplayNames(undefined, { type: "language" });
      const match = displayNames.of(normalized);
      if (match) return match;
    } catch {
      // Fall back to the raw code below.
    }
  }

  return normalized.toUpperCase();
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
