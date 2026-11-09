import type { FileVersion, VersionVideoTrack } from "@/api/types";
import {
  dolbyVisionLabel,
  formatBitrate,
  formatChannels,
  formatFileSize,
  formatSampleRate,
  mapAudioLabel,
} from "@/lib/mediaFormat";
import { type MessageKey, numberText, t } from "@/i18n";
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
// Each detail is "EL" or the base-layer range it stays compatible with.
const DV_RANGE_TYPE_DETAILS: Record<string, { el: true } | { compatible: string }> = {
  DOVIWithEL: { el: true },
  DOVIWithELHDR10Plus: { el: true },
  DOVIWithHDR10: { compatible: "HDR10" },
  DOVIWithHDR10Plus: { compatible: "HDR10" },
  DOVIWithSDR: { compatible: "SDR" },
  DOVIWithHLG: { compatible: "HLG" },
};

function compatibleWith(range: string): string {
  return t("web.detail.mediaInfo.rangeCompatible", { range });
}

export function formatDolbyVisionLabel(track: VersionVideoTrack): string {
  const raw = track.dolby_vision?.trim() ?? "";
  if (!raw && !track.dv_profile) return "";

  const base = raw
    ? dolbyVisionLabel(raw)
    : t("web.detail.mediaInfo.dolbyVisionProfile", { profile: String(track.dv_profile) });

  const compat = track.dv_bl_compat_id != null ? DV_BL_COMPAT_LABELS[track.dv_bl_compat_id] : "";
  const details = [compat ? compatibleWith(compat) : "", track.dv_el_present ? "EL" : ""].filter(
    Boolean,
  );
  if (details.length === 0) {
    const rangeDetail = DV_RANGE_TYPE_DETAILS[track.video_range_type?.trim() ?? ""];
    if (rangeDetail)
      details.push("el" in rangeDetail ? "EL" : compatibleWith(rangeDetail.compatible));
  }

  return details.length > 0 ? `${base} (${details.join(", ")})` : base;
}

// Friendly names for the Jellyfin-style video_range_type enum the scanner derives.
// Called when shown: the labels with words in them are translated.
function videoRangeTypeLabel(rangeType: string): string | undefined {
  const withEl = () => t("web.detail.mediaInfo.dolbyVisionWithEl");
  const compatible = (range: string) => t("web.detail.mediaInfo.dolbyVisionCompatible", { range });
  switch (rangeType) {
    case "SDR":
      return "SDR";
    case "HDR10":
      return "HDR10";
    case "HDR10Plus":
      return "HDR10+";
    case "HLG":
      return "HLG";
    case "DOVI":
      return "Dolby Vision";
    case "DOVIWithEL":
      return withEl();
    case "DOVIWithELHDR10Plus":
      return `${withEl()} · HDR10+`;
    case "DOVIWithHDR10":
      return compatible("HDR10");
    case "DOVIWithHDR10Plus":
      return "Dolby Vision · HDR10+";
    case "DOVIWithHLG":
      return compatible("HLG");
    case "DOVIWithSDR":
      return compatible("SDR");
    default:
      return undefined;
  }
}

export function formatVideoRangeLabel(track: VersionVideoTrack): string {
  const dolbyVision = formatDolbyVisionLabel(track);
  if (dolbyVision) {
    return track.hdr10_plus ? `${dolbyVision} · HDR10+` : dolbyVision;
  }

  const rangeType = track.video_range_type?.trim();
  if (rangeType) {
    const label = videoRangeTypeLabel(rangeType) ?? rangeType;
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

function formatDurationSeconds(seconds?: number): string {
  if (!seconds || seconds <= 0) return "";
  const total = Math.floor(seconds);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) {
    return t("web.detail.mediaInfo.durationHms", {
      hours: h,
      minutes: m,
      seconds: s,
    });
  }
  if (m > 0) return t("web.detail.mediaInfo.durationMs", { minutes: m, seconds: s });
  return t("web.detail.mediaInfo.durationS", { seconds: s });
}

function pushRow(rows: MediaSpecRow[], label: MessageKey, value?: string | false) {
  if (value) rows.push({ label: t(label), value });
}

function yesIf(flag?: boolean): string {
  return flag ? t("web.detail.mediaInfo.yes") : "";
}

function sectionTitle(
  kind: MessageKey,
  numbered: MessageKey,
  index: number,
  total: number,
): string {
  return total > 1 ? t(numbered, { number: index + 1 }) : t(kind);
}

function trackTitle(track: { title?: string; embedded_title?: string }): string {
  return track.title ?? track.embedded_title ?? "";
}

export function buildGeneralSection(version: FileVersion): MediaSpecSection {
  const rows: MediaSpecRow[] = [];
  pushRow(
    rows,
    "web.detail.mediaInfo.row.container",
    version.container ? version.container.toUpperCase() : "",
  );
  pushRow(rows, "web.detail.mediaInfo.row.fileSize", formatFileSize(version.file_size));
  pushRow(rows, "web.detail.mediaInfo.row.duration", formatDurationSeconds(version.duration));
  pushRow(rows, "web.detail.mediaInfo.row.overallBitrate", formatBitrate(version.bitrate));
  pushRow(rows, "web.detail.mediaInfo.row.edition", version.edition_raw);
  pushRow(rows, "web.detail.mediaInfo.row.added", formatAddedAt(version.added_at));
  pushRow(rows, "web.detail.mediaInfo.row.filePath", version.file_path?.trim());
  return { title: t("web.detail.mediaInfo.section.general"), rows };
}

export function buildVideoSections(version: FileVersion): MediaSpecSection[] {
  const tracks = version.video_tracks ?? [];
  return tracks.map((track, index) => {
    const rows: MediaSpecRow[] = [];
    pushRow(rows, "web.detail.mediaInfo.row.codec", track.codec ? track.codec.toUpperCase() : "");
    pushRow(rows, "web.detail.mediaInfo.row.profile", track.profile);
    pushRow(rows, "web.detail.mediaInfo.row.level", formatVideoLevel(track.codec, track.level));
    pushRow(
      rows,
      "web.detail.mediaInfo.row.resolution",
      track.width && track.height ? `${track.width}x${track.height}` : "",
    );
    pushRow(rows, "web.detail.mediaInfo.row.aspectRatio", track.aspect_ratio);
    pushRow(
      rows,
      "web.detail.mediaInfo.row.frameRateLabel",
      track.frame_rate
        ? t("web.detail.mediaInfo.frameRate", { rate: numberText(track.frame_rate) })
        : "",
    );
    pushRow(rows, "web.detail.mediaInfo.row.bitrate", formatBitrate(track.bitrate));
    pushRow(
      rows,
      "web.detail.mediaInfo.row.bitDepthLabel",
      track.bit_depth ? t("web.detail.mediaInfo.bitDepth", { depth: track.bit_depth }) : "",
    );
    pushRow(rows, "web.detail.mediaInfo.row.pixelFormat", track.pixel_format);
    pushRow(
      rows,
      "web.detail.mediaInfo.row.chromaSubsampling",
      formatChromaSubsampling(track.pixel_format),
    );
    pushRow(rows, "web.detail.mediaInfo.row.dynamicRange", formatVideoRangeLabel(track));
    pushRow(rows, "web.detail.mediaInfo.row.colorPrimaries", track.color_primaries);
    pushRow(rows, "web.detail.mediaInfo.row.colorTransfer", track.color_transfer);
    pushRow(rows, "web.detail.mediaInfo.row.colorSpace", track.color_space);
    pushRow(
      rows,
      "web.detail.mediaInfo.row.referenceFrames",
      track.reference_frames ? String(track.reference_frames) : "",
    );
    pushRow(
      rows,
      "web.detail.mediaInfo.row.scan",
      track.interlaced
        ? t("web.detail.mediaInfo.interlaced")
        : t("web.detail.mediaInfo.progressive"),
    );
    return {
      title: sectionTitle(
        "web.detail.mediaInfo.video",
        "web.detail.mediaInfo.section.videoNumbered",
        index,
        tracks.length,
      ),
      rows,
    };
  });
}

export function buildAudioSections(version: FileVersion): MediaSpecSection[] {
  const tracks = version.audio_tracks ?? [];
  return tracks.map((track, index) => {
    const rows: MediaSpecRow[] = [];
    pushRow(rows, "web.detail.mediaInfo.row.titleLabel", trackTitle(track));
    pushRow(rows, "web.detail.mediaInfo.row.language", formatLanguageName(track.language));
    pushRow(rows, "web.detail.mediaInfo.row.codec", track.codec ? mapAudioLabel(track.codec) : "");
    pushRow(rows, "web.detail.mediaInfo.row.profile", track.profile);
    pushRow(rows, "web.detail.mediaInfo.row.layout", track.layout);
    pushRow(rows, "web.detail.mediaInfo.row.channels", formatChannels(track.channels));
    pushRow(rows, "web.detail.mediaInfo.row.bitrate", formatBitrate(track.bitrate));
    pushRow(rows, "web.detail.mediaInfo.row.sampleRate", formatSampleRate(track.sample_rate));
    pushRow(
      rows,
      "web.detail.mediaInfo.row.bitDepthLabel",
      track.bit_depth ? t("web.detail.mediaInfo.bitDepth", { depth: track.bit_depth }) : "",
    );
    pushRow(rows, "web.detail.mediaInfo.row.defaultLabel", yesIf(track.default));
    return {
      title: sectionTitle(
        "web.detail.mediaInfo.audio",
        "web.detail.mediaInfo.section.audioNumbered",
        index,
        tracks.length,
      ),
      rows,
    };
  });
}

export function buildSubtitleSections(version: FileVersion): MediaSpecSection[] {
  const tracks = version.subtitle_tracks ?? [];
  return tracks.map((track, index) => {
    const rows: MediaSpecRow[] = [];
    pushRow(rows, "web.detail.mediaInfo.row.titleLabel", trackTitle(track));
    pushRow(rows, "web.detail.mediaInfo.row.language", formatLanguageName(track.language));
    pushRow(rows, "web.detail.mediaInfo.row.format", track.codec ? track.codec.toUpperCase() : "");
    pushRow(
      rows,
      "web.detail.mediaInfo.row.source",
      track.external ? t("web.detail.mediaInfo.external") : t("web.detail.mediaInfo.embedded"),
    );
    pushRow(rows, "web.detail.mediaInfo.row.file", track.external ? track.file_name : "");
    pushRow(rows, "web.detail.mediaInfo.row.resolution", track.resolution);
    pushRow(rows, "web.detail.mediaInfo.row.forcedLabel", yesIf(track.forced));
    pushRow(rows, "web.detail.mediaInfo.row.defaultLabel", yesIf(track.default));
    pushRow(rows, "web.detail.mediaInfo.row.hearingImpaired", yesIf(track.hearing_impaired));
    return {
      title: sectionTitle(
        "web.detail.mediaInfo.subtitle",
        "web.detail.mediaInfo.section.subtitleNumbered",
        index,
        tracks.length,
      ),
      rows,
    };
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
