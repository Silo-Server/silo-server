// Canonical media-spec formatting helpers shared by item detail views, the
// player stats overlay, poster overlays, and admin views. Display policy
// differences between surfaces (empty-string vs "—" fallback, GB vs GiB
// labels) are expressed via parameters here instead of per-surface copies.

import type { VideoRangeSource } from "@/lib/videoRange";
import { videoRangeLabel } from "@/lib/videoRange";

export const CODEC_LABELS: Record<string, string> = {
  aac: "AAC",
  ac3: "AC3",
  av1: "AV1",
  dts: "DTS",
  dtshd: "DTS-HD",
  eac3: "Dolby Digital+",
  flac: "FLAC",
  h264: "H.264",
  hevc: "HEVC",
  mp3: "MP3",
  opus: "Opus",
  truehd: "TrueHD",
};

/** Exact-match codec display label; unknown codecs are uppercased. */
export function formatCodecLabel(codec?: string, fallback = "—"): string {
  const normalized = codec?.trim();
  if (!normalized) {
    return fallback;
  }
  return CODEC_LABELS[normalized.toLowerCase()] ?? normalized.toUpperCase();
}

/** Fuzzy audio-family label for quality badges ("truehd atmos" → "Atmos"). */
export function mapAudioLabel(codec: string): string {
  const lower = codec.toLowerCase();
  if (lower.includes("atmos")) return "Atmos";
  if (lower.includes("truehd")) return "TrueHD";
  if (lower.includes("dts-hd") || lower.includes("dts:x")) return "DTS-HD";
  if (lower.includes("dts")) return "DTS";
  if (lower.includes("eac3") || lower.includes("e-ac-3")) return "EAC3";
  if (lower.includes("aac")) return "AAC";
  if (lower.includes("flac")) return "FLAC";
  return codec.toUpperCase();
}

interface AudioFormatTrack {
  codec?: string;
  profile?: string;
  layout?: string;
  title?: string;
  embedded_title?: string;
  default?: boolean;
}

interface AudioFormatVersion {
  codec_audio?: string;
  effective_audio_track_index?: number;
  audio_tracks?: readonly AudioFormatTrack[];
}

interface QualitySummaryVersion extends VideoRangeSource, AudioFormatVersion {
  resolution?: string;
  codec_video?: string;
}

export function formatDynamicRangeLabel(version: VideoRangeSource): string | null {
  const label = videoRangeLabel(version);
  if (label.startsWith("DV")) return "Dolby Vision";
  return label || null;
}

export function formatAudioTrackLabel(track: AudioFormatTrack | undefined): string {
  if (!track) return "";

  const details = [track.codec, track.profile, track.layout, track.title, track.embedded_title]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  const hasAtmos = details.includes("atmos") || details.includes("joc");

  if (hasAtmos) {
    if (
      details.includes("eac3") ||
      details.includes("e-ac-3") ||
      details.includes("ec-3") ||
      details.includes("dolby digital plus") ||
      details.includes("dd+")
    ) {
      return "DD+ Atmos";
    }
    if (details.includes("truehd")) return "TrueHD Atmos";
    return "Atmos";
  }

  return track.codec?.trim() ? mapAudioLabel(track.codec) : "";
}

export function formatVersionAudioLabel(version: AudioFormatVersion): string {
  const tracks = version.audio_tracks ?? [];
  const effectiveIndex = version.effective_audio_track_index;
  const effectiveTrack =
    effectiveIndex != null && effectiveIndex >= 0 && effectiveIndex < tracks.length
      ? tracks[effectiveIndex]
      : undefined;
  const track = effectiveTrack ?? tracks.find((candidate) => candidate.default) ?? tracks[0];

  return formatAudioTrackLabel(track) || formatAudioTrackLabel({ codec: version.codec_audio });
}

function videoQualityParts(version: QualitySummaryVersion): Array<string | null> {
  return [
    prettyResolution(version.resolution),
    formatCodecLabel(version.codec_video, ""),
    formatDynamicRangeLabel(version),
  ];
}

export function formatVideoQualitySummary(
  version: QualitySummaryVersion,
  separator = " · ",
): string {
  return videoQualityParts(version).filter(Boolean).join(separator);
}

export function formatVersionQualitySummary(
  version: QualitySummaryVersion,
  separator = " · ",
): string {
  return [...videoQualityParts(version), formatVersionAudioLabel(version)]
    .filter(Boolean)
    .join(separator);
}

interface FormatFileSizeOptions {
  fallback?: string;
  /** Use IEC unit labels (GiB/MiB/KiB) instead of GB/MB/KB. Math is 1024-based either way. */
  iecUnits?: boolean;
}

export function formatFileSize(bytes?: number, options: FormatFileSizeOptions = {}): string {
  const { fallback = "", iecUnits = false } = options;
  if (!isPositive(bytes)) return fallback;
  const [giga, mega, kilo] = iecUnits ? ["GiB", "MiB", "KiB"] : ["GB", "MB", "KB"];
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} ${giga}`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(1)} ${mega}`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} ${kilo}`;
  return `${bytes} B`;
}

export function formatBitrate(kbps?: number, fallback = ""): string {
  if (!isPositive(kbps)) return fallback;
  return `${Math.round(kbps).toLocaleString()} kbps`;
}

export function formatMbpsFromKbps(kbps?: number, fallback = "—"): string {
  if (!isPositive(kbps)) return fallback;
  return `${(kbps / 1000).toFixed(1)} Mbps`;
}

export function formatSampleRate(sampleRate?: number, fallback = ""): string {
  if (!isPositive(sampleRate)) return fallback;
  return `${sampleRate.toLocaleString()} Hz`;
}

export function formatChannels(channels?: number): string {
  if (!channels || channels <= 0) return "";
  if (channels === 8) return "7.1";
  if (channels === 6) return "5.1";
  if (channels === 2) return "stereo";
  return `${channels} ch`;
}

/** Ensures a raw Dolby Vision label ("Profile 8.1") carries the "Dolby Vision" prefix. */
export function dolbyVisionLabel(raw: string): string {
  return raw.toLowerCase().startsWith("dolby vision") ? raw : `Dolby Vision ${raw}`;
}

// Kometa-style pretty resolution label: "2160p" → "4K", others keep their
// lowercase-p form ("1080p", "720p"). Unknown values are uppercased so they
// at least look intentional.
export function prettyResolution(value: string | undefined): string | null {
  if (!value) return null;
  const v = value.toLowerCase().trim();
  if (v === "") return null;
  if (v === "2160p" || v === "4k" || v === "uhd") return "4K";
  if (v === "4320p" || v === "8k") return "8K";
  if (/^\d+p$/.test(v)) return v;
  return value.toUpperCase();
}

// Compact HDR suffix used for combined Resolution+HDR badges. Any Dolby
// Vision variant collapses to "DV"; any other HDR variant to "HDR".
export function compactHdrSuffix(value: string | undefined): string | null {
  if (!value) return null;
  if (value.includes("DV")) return "DV";
  return "HDR";
}

function isPositive(value?: number): value is number {
  return Number.isFinite(value) && value != null && value > 0;
}

/** Hero runtime badge label ("2h 43m", "43m"); "" for zero/unknown. */
export function formatRuntimeMinutes(minutes: number): string {
  if (minutes <= 0) return "";
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}
