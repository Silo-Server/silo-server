import { type ReactNode, useState } from "react";
import type { VersionAudioTrack, VersionSubtitleTrack, VersionVideoTrack } from "@/api/types";

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

export function formatFileSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "";
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${bytes} B`;
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

export function formatChannels(channels?: number): string {
  if (!channels || channels <= 0) return "";
  if (channels === 8) return "7.1";
  if (channels === 6) return "5.1";
  if (channels === 2) return "stereo";
  return `${channels} ch`;
}

export function formatBitrate(bitrate?: number): string {
  if (!bitrate || bitrate <= 0) return "";
  return `${bitrate.toLocaleString()} kbps`;
}

export function formatSampleRate(sampleRate?: number): string {
  if (!sampleRate || sampleRate <= 0) return "";
  return `${sampleRate.toLocaleString()} Hz`;
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

const TRACK_COLLAPSE_THRESHOLD = 3;

export function TrackSection({
  label,
  count,
  children,
}: {
  label: string;
  count: number;
  children: ReactNode[];
}) {
  const [expanded, setExpanded] = useState(false);
  const collapsible = count > TRACK_COLLAPSE_THRESHOLD;
  const visible = collapsible && !expanded ? children.slice(0, TRACK_COLLAPSE_THRESHOLD) : children;
  const hiddenCount = count - TRACK_COLLAPSE_THRESHOLD;

  return (
    <div>
      <div className="text-muted-foreground/60 mb-1 text-[11px] font-medium">{label}</div>
      <div className="divide-border/10 divide-y">{visible}</div>
      {collapsible && (
        <button
          type="button"
          onClick={() => setExpanded((prev) => !prev)}
          className="text-muted-foreground hover:text-foreground/70 mt-1 text-xs font-medium transition-colors"
        >
          {expanded ? "Show less" : `+${hiddenCount} more`}
        </button>
      )}
    </div>
  );
}

export function TrackRow({ title, meta }: { title: string; meta: string }) {
  return (
    <div className="py-1.5">
      <div className="text-foreground/85 text-sm">{title}</div>
      {meta && <div className="text-muted-foreground mt-0.5 text-xs">{meta}</div>}
    </div>
  );
}
