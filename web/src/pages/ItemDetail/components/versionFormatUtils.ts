import type {
  FileVersion,
  VersionAudioTrack,
  VersionSubtitleTrack,
  VersionVideoTrack,
} from "@/api/types";
import { displayLanguageName, getLanguageName } from "@/lib/languageNames";
import {
  audioTrackAtmosEvidence,
  formatBitrate,
  formatChannels,
  formatSampleRate,
} from "@/lib/mediaFormat";
import { formatLocale, pinnedLocaleUnlessLocalized } from "@/lib/appLocale";
import { t } from "@/i18n";

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
  return t("web.detail.ebook.pageCount", {
    count: pages,
    count_text: pages.toLocaleString(formatLocale()),
  });
}

export function formatAddedAt(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return new Intl.DateTimeFormat(pinnedLocaleUnlessLocalized("en-US"), {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(date);
}

export function formatLanguageName(language?: string): string {
  if (!language) return "";

  const trimmed = language.trim();
  const normalized = trimmed.toLowerCase();
  if (!normalized) return "";

  const standardizedName = displayLanguageName(trimmed);
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

export function metadataLine(parts: Array<string | undefined | false>): string {
  return parts.filter(Boolean).join(" \u00B7 ");
}

export function videoTitle(track: VersionVideoTrack): string {
  return (
    track.title ||
    [track.width && track.height ? `${track.width}x${track.height}` : "", track.codec]
      .filter(Boolean)
      .join(" ") ||
    t("web.detail.mediaInfo.video")
  );
}

export function audioTitle(track: VersionAudioTrack): string {
  return (
    track.title ||
    track.embedded_title ||
    [track.language, track.codec, formatChannels(track.channels)].filter(Boolean).join(" ") ||
    t("web.detail.mediaInfo.audio")
  );
}

export function subtitleTitle(track: VersionSubtitleTrack): string {
  const language = formatLanguageName(track.language);
  const title =
    track.title || track.embedded_title || track.codec || t("web.detail.mediaInfo.subtitle");

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
    track.frame_rate ? t("web.detail.mediaInfo.frameRate", { rate: String(track.frame_rate) }) : "",
    formatBitrate(track.bitrate),
    track.bit_depth ? t("web.detail.mediaInfo.bitDepth", { depth: track.bit_depth }) : "",
    track.video_range,
    track.dolby_vision,
    track.color_space,
    track.interlaced ? t("web.detail.mediaInfo.interlaced") : "",
  ]);
}

export function compactAudioMeta(track: VersionAudioTrack): string {
  return metadataLine([
    track.layout,
    formatBitrate(track.bitrate),
    formatSampleRate(track.sample_rate),
    track.bit_depth ? t("web.detail.mediaInfo.bitDepth", { depth: track.bit_depth }) : "",
  ]);
}

export function compactSubtitleMeta(track: VersionSubtitleTrack): string {
  return metadataLine([
    track.external ? t("web.detail.mediaInfo.external") : t("web.detail.mediaInfo.embedded"),
    track.forced ? t("web.detail.mediaInfo.forced") : "",
    track.hearing_impaired ? t("web.detail.mediaInfo.hearingImpairedShort") : "",
    track.default ? t("web.detail.mediaInfo.defaultBadge") : "",
    track.resolution,
  ]);
}
