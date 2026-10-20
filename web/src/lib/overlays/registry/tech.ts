import { compactHdrSuffix, prettyResolution } from "@/lib/mediaFormat";
import type { OverlayDef, OverlayIconId } from "../types";

// Tech overlays — derived from the media file (codec, container, resolution,
// audio properties). All data flows through OverlaySummary on the backend.

// hdrIcon picks the badge icon for a dynamic-range value. Wordmark icons
// (hdr10, hdr) replace the text label when they spell the same thing (see
// WORDMARK_TEXT), so they are only returned for values they fully express;
// HLG has no mark and renders as a plain text label.
function hdrIcon(value: string | undefined): OverlayIconId | null {
  if (!value) return null;
  if (value.includes("DV")) return "dolby-vision";
  if (value === "HDR10") return "hdr10";
  if (value === "HDR") return "hdr";
  return null;
}

function audioIcon(value: string | undefined): OverlayIconId | null {
  if (!value) return null;
  if (value.toLowerCase() === "atmos") return "atmos";
  return "volume";
}

function videoCodecIcon(value: string | undefined): OverlayIconId | null {
  if (!value) return null;
  if (value === "AV1") return "av1";
  return "film";
}

export const TECH_OVERLAYS: readonly OverlayDef[] = [
  {
    id: "resolution",
    category: "tech",
    label: "Resolution",
    description: "Video resolution (4K, 1080p, 720p, etc.)",
    defaultPosition: "top-left",
    defaultEnabled: true,
    iconId: "monitor",
    iconCapable: true,
    getValue: (d) => prettyResolution(d.resolution),
    getValueKey: (d) => d.resolution || null,
    valueKeySuggestions: ["2160p", "1080p", "720p", "480p"],
  },
  {
    id: "hdr",
    category: "tech",
    label: "HDR / Dolby Vision",
    description: "Dynamic range format (HDR10, DV, HLG)",
    defaultPosition: "top-left",
    defaultEnabled: true,
    iconCapable: true,
    getValue: (d) => d.hdr ?? null,
    getValueKey: (d) => d.hdr || null,
    valueKeySuggestions: ["DV", "DV HDR10", "DV HDR10+", "HDR10", "HDR10+", "HLG", "HDR"],
    getIcon: (d) => hdrIcon(d.hdr),
  },
  {
    id: "resolution_hdr",
    category: "tech",
    label: "Resolution + HDR (combined)",
    description: 'Single badge combining resolution and dynamic range (e.g. "4K DV", "1080p HDR")',
    defaultPosition: "top-left",
    defaultEnabled: false,
    iconCapable: true,
    getValue: (d) => {
      const res = prettyResolution(d.resolution);
      if (!res) return null;
      const hdr = compactHdrSuffix(d.hdr);
      return hdr ? `${res} ${hdr}` : res;
    },
    // Only the DV circle mark works next to a combined label; a wordmark
    // (HDR10) would visually duplicate the label's HDR suffix.
    getIcon: (d) => (d.hdr?.includes("DV") ? "dolby-vision" : null),
    // The resolution and dynamic range as the API returns them, joined by a
    // space: "2160p DV HDR10", or "1080p" without HDR.
    getValueKey: (d) => (d.resolution ? [d.resolution, d.hdr].filter(Boolean).join(" ") : null),
  },
  {
    id: "audio",
    category: "tech",
    label: "Audio Codec",
    description: "Audio codec (Atmos, DTS-HD, TrueHD, etc.)",
    defaultPosition: "top-left",
    defaultEnabled: true,
    iconCapable: true,
    getValue: (d) => d.audio ?? null,
    getValueKey: (d) => d.audio || null,
    valueKeySuggestions: ["Atmos", "TrueHD", "DTS-HD", "DTS", "EAC3", "AC3", "AAC", "FLAC"],
    getIcon: (d) => audioIcon(d.audio),
  },
  {
    id: "audio_channels",
    category: "tech",
    label: "Audio Channels",
    description: "Channel layout (Stereo, 5.1, 7.1)",
    defaultPosition: "top-left",
    defaultEnabled: false,
    iconId: "volume",
    iconCapable: true,
    getValue: (d) => d.audio_channels ?? null,
    getValueKey: (d) => d.audio_channels || null,
  },
  {
    id: "video_codec",
    category: "tech",
    label: "Video Codec",
    description: "Video codec (H.264, H.265, AV1)",
    defaultPosition: "top-left",
    defaultEnabled: false,
    iconId: "film",
    iconCapable: true,
    getValue: (d) => d.video_codec ?? null,
    getValueKey: (d) => d.video_codec || null,
    valueKeySuggestions: ["H.265", "H.264", "AV1", "VP9"],
    getIcon: (d) => videoCodecIcon(d.video_codec),
  },
  {
    id: "container",
    category: "tech",
    label: "Container",
    description: "File container (MKV, MP4, etc.)",
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconCapable: false,
    getValue: (d) => d.container ?? null,
    getValueKey: (d) => d.container || null,
  },
  {
    id: "aspect_ratio",
    category: "tech",
    label: "Aspect Ratio",
    description: "Display aspect ratio (16:9, 2.39:1, etc.)",
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "layout",
    iconCapable: true,
    getValue: (d) => d.aspect_ratio ?? null,
    getValueKey: (d) => d.aspect_ratio || null,
  },
  {
    id: "release_type",
    category: "tech",
    label: "Release Type",
    description: "Source format (REMUX, BluRay, WEB-DL, etc.)",
    defaultPosition: "bottom-left",
    defaultEnabled: true,
    iconCapable: false,
    getValue: (d) => d.release_type ?? null,
    getValueKey: (d) => d.release_type || null,
    valueKeySuggestions: ["REMUX", "BluRay", "WEB-DL", "WEBRip", "HDTV", "DVD"],
  },
  {
    id: "edition",
    category: "tech",
    label: "Edition",
    description: "Edition label from the best available media version",
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconCapable: false,
    getValue: (d) => d.edition ?? null,
    getValueKey: (d) => d.edition || null,
  },
  {
    id: "multi_audio",
    category: "tech",
    label: "Multi-Audio",
    description: "Shown when the file has audio in 2+ languages",
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "languages",
    iconCapable: true,
    getValue: (d) => (d.multi_audio ? "Multi-Audio" : null),
  },
  {
    id: "multi_sub",
    category: "tech",
    label: "Subtitles Available",
    description: "Shown when the file has any subtitle track",
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "subtitles",
    iconCapable: true,
    getValue: (d) => (d.multi_sub ? "CC" : null),
  },
];
