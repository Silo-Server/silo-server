import type { FileVersion, VersionVideoTrack } from "@/api/types";
import {
  dolbyVisionLabel,
  formatBitrate,
  formatChannels,
  formatFileSize,
  formatSampleRate,
  mapAudioLabel,
} from "@/lib/mediaFormat";
import { formatAddedAt, formatLanguageName } from "./versionFormatUtils";

export interface MediaSpecRow {
  label: string;
  value: string;
}

export interface MediaSpecSection {
  title: string;
  rows: MediaSpecRow[];
}

const CHROMA_SUBSAMPLING_PATTERNS: Array<{ pattern: RegExp; label: string }> = [
  { pattern: /444/, label: "4:4:4" },
  { pattern: /440/, label: "4:4:0" },
  { pattern: /422/, label: "4:2:2" },
  { pattern: /420|^nv12|^nv21|^p010|^p016/, label: "4:2:0" },
  { pattern: /411/, label: "4:1:1" },
  { pattern: /410/, label: "4:1:0" },
];

export function formatChromaSubsampling(pixelFormat?: string): string {
  const normalized = pixelFormat?.trim().toLowerCase();
  if (!normalized) return "";
  for (const { pattern, label } of CHROMA_SUBSAMPLING_PATTERNS) {
    if (pattern.test(normalized)) return label;
  }
  return "";
}

const DV_BL_COMPAT_LABELS: Record<number, string> = {
  1: "HDR10",
  2: "SDR",
  4: "HLG",
  6: "HDR10",
};

// Compatibility details recoverable from the scanner-derived video_range_type
// enum when ffprobe omitted the explicit DV side-data fields (probe.go still
// classifies profile-8 files via color_transfer in that case).
const DV_RANGE_TYPE_DETAILS: Record<string, string> = {
  DOVIWithEL: "EL",
  DOVIWithELHDR10Plus: "EL",
  DOVIWithHDR10: "HDR10 compatible",
  DOVIWithSDR: "SDR compatible",
  DOVIWithHLG: "HLG compatible",
};

export function formatDolbyVisionLabel(track: VersionVideoTrack): string {
  const raw = track.dolby_vision?.trim() ?? "";
  if (!raw && !track.dv_profile) return "";

  const base = raw ? dolbyVisionLabel(raw) : `Dolby Vision Profile ${track.dv_profile}`;

  const compat = track.dv_bl_compat_id != null ? DV_BL_COMPAT_LABELS[track.dv_bl_compat_id] : "";
  const details = [compat ? `${compat} compatible` : "", track.dv_el_present ? "EL" : ""].filter(
    Boolean,
  );
  if (details.length === 0) {
    const rangeDetail = DV_RANGE_TYPE_DETAILS[track.video_range_type?.trim() ?? ""];
    if (rangeDetail) details.push(rangeDetail);
  }

  return details.length > 0 ? `${base} (${details.join(", ")})` : base;
}

// Friendly names for the Jellyfin-style video_range_type enum the scanner derives.
const VIDEO_RANGE_TYPE_LABELS: Record<string, string> = {
  SDR: "SDR",
  HDR10: "HDR10",
  HDR10Plus: "HDR10+",
  HLG: "HLG",
  DOVI: "Dolby Vision",
  DOVIWithEL: "Dolby Vision (with EL)",
  DOVIWithELHDR10Plus: "Dolby Vision (with EL) · HDR10+",
  DOVIWithHDR10: "Dolby Vision (HDR10 compatible)",
  DOVIWithHDR10Plus: "Dolby Vision · HDR10+",
  DOVIWithHLG: "Dolby Vision (HLG compatible)",
  DOVIWithSDR: "Dolby Vision (SDR compatible)",
};

export function formatVideoRangeLabel(track: VersionVideoTrack): string {
  const dolbyVision = formatDolbyVisionLabel(track);
  if (dolbyVision) {
    return track.hdr10_plus ? `${dolbyVision} · HDR10+` : dolbyVision;
  }

  const rangeType = track.video_range_type?.trim();
  if (rangeType) {
    const label = VIDEO_RANGE_TYPE_LABELS[rangeType] ?? rangeType;
    return track.hdr10_plus && !label.includes("HDR10+") ? `${label} · HDR10+` : label;
  }

  return track.hdr10_plus ? "HDR10+" : (track.video_range?.trim() ?? "");
}

export function formatVideoLevel(codec?: string, level?: number): string {
  if (!level || level <= 0) return "";
  const normalized = codec?.toLowerCase() ?? "";
  if (normalized.includes("hevc") || normalized.includes("h265") || normalized.includes("265")) {
    return trimLevel(level / 30);
  }
  if (normalized.includes("avc") || normalized.includes("h264") || normalized.includes("264")) {
    return trimLevel(level / 10);
  }
  if (normalized.includes("av1")) {
    return `${2 + (level >> 2)}.${level & 3}`;
  }
  return String(level);
}

function trimLevel(value: number): string {
  const rounded = Math.round(value * 10) / 10;
  return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
}

export function formatDurationSeconds(seconds?: number): string {
  if (!seconds || seconds <= 0) return "";
  const total = Math.floor(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}h ${m}m ${s}s`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

function pushRow(rows: MediaSpecRow[], label: string, value?: string | false) {
  if (value) rows.push({ label, value });
}

function yesIf(flag?: boolean): string {
  return flag ? "Yes" : "";
}

function sectionTitle(kind: string, index: number, total: number): string {
  return total > 1 ? `${kind} ${index + 1}` : kind;
}

function trackTitle(track: { title?: string; embedded_title?: string }): string {
  return track.title ?? track.embedded_title ?? "";
}

export function buildGeneralSection(version: FileVersion): MediaSpecSection {
  const rows: MediaSpecRow[] = [];
  pushRow(rows, "Container", version.container ? version.container.toUpperCase() : "");
  pushRow(rows, "File Size", formatFileSize(version.file_size));
  pushRow(rows, "Duration", formatDurationSeconds(version.duration));
  pushRow(rows, "Overall Bitrate", formatBitrate(version.bitrate));
  pushRow(rows, "Edition", version.edition_raw);
  pushRow(rows, "Added", formatAddedAt(version.added_at));
  pushRow(rows, "File Path", version.file_path?.trim());
  return { title: "General", rows };
}

export function buildVideoSections(version: FileVersion): MediaSpecSection[] {
  const tracks = version.video_tracks ?? [];
  return tracks.map((track, index) => {
    const rows: MediaSpecRow[] = [];
    pushRow(rows, "Codec", track.codec ? track.codec.toUpperCase() : "");
    pushRow(rows, "Profile", track.profile);
    pushRow(rows, "Level", formatVideoLevel(track.codec, track.level));
    pushRow(
      rows,
      "Resolution",
      track.width && track.height ? `${track.width}x${track.height}` : "",
    );
    pushRow(rows, "Aspect Ratio", track.aspect_ratio);
    pushRow(rows, "Frame Rate", track.frame_rate ? `${track.frame_rate} fps` : "");
    pushRow(rows, "Bitrate", formatBitrate(track.bitrate));
    pushRow(rows, "Bit Depth", track.bit_depth ? `${track.bit_depth}-bit` : "");
    pushRow(rows, "Pixel Format", track.pixel_format);
    pushRow(rows, "Chroma Subsampling", formatChromaSubsampling(track.pixel_format));
    pushRow(rows, "Dynamic Range", formatVideoRangeLabel(track));
    pushRow(rows, "Color Primaries", track.color_primaries);
    pushRow(rows, "Color Transfer", track.color_transfer);
    pushRow(rows, "Color Space", track.color_space);
    pushRow(rows, "Reference Frames", track.reference_frames ? String(track.reference_frames) : "");
    pushRow(rows, "Scan", track.interlaced ? "Interlaced" : "Progressive");
    return { title: sectionTitle("Video", index, tracks.length), rows };
  });
}

export function buildAudioSections(version: FileVersion): MediaSpecSection[] {
  const tracks = version.audio_tracks ?? [];
  return tracks.map((track, index) => {
    const rows: MediaSpecRow[] = [];
    pushRow(rows, "Title", trackTitle(track));
    pushRow(rows, "Language", formatLanguageName(track.language));
    pushRow(rows, "Codec", track.codec ? mapAudioLabel(track.codec) : "");
    pushRow(rows, "Profile", track.profile);
    pushRow(rows, "Layout", track.layout);
    pushRow(rows, "Channels", formatChannels(track.channels));
    pushRow(rows, "Bitrate", formatBitrate(track.bitrate));
    pushRow(rows, "Sample Rate", formatSampleRate(track.sample_rate));
    pushRow(rows, "Bit Depth", track.bit_depth ? `${track.bit_depth}-bit` : "");
    pushRow(rows, "Default", yesIf(track.default));
    return { title: sectionTitle("Audio", index, tracks.length), rows };
  });
}

export function buildSubtitleSections(version: FileVersion): MediaSpecSection[] {
  const tracks = version.subtitle_tracks ?? [];
  return tracks.map((track, index) => {
    const rows: MediaSpecRow[] = [];
    pushRow(rows, "Title", trackTitle(track));
    pushRow(rows, "Language", formatLanguageName(track.language));
    pushRow(rows, "Format", track.codec ? track.codec.toUpperCase() : "");
    pushRow(rows, "Source", track.external ? "External" : "Embedded");
    pushRow(rows, "File", track.external ? track.file_name : "");
    pushRow(rows, "Resolution", track.resolution);
    pushRow(rows, "Forced", yesIf(track.forced));
    pushRow(rows, "Default", yesIf(track.default));
    pushRow(rows, "Hearing Impaired", yesIf(track.hearing_impaired));
    return { title: sectionTitle("Subtitle", index, tracks.length), rows };
  });
}

export function buildMediaSpecSections(version: FileVersion): MediaSpecSection[] {
  return [
    buildGeneralSection(version),
    ...buildVideoSections(version),
    ...buildAudioSections(version),
    ...buildSubtitleSections(version),
  ].filter((section) => section.rows.length > 0);
}
