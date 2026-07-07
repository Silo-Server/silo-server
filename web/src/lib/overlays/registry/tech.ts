import { compactHdrSuffix, prettyResolution } from "@/lib/mediaFormat";
import type { OverlayDef, OverlayIconId } from "../types";

// Tech overlays — derived from the media file (codec, container, resolution,
// audio properties). All data flows through OverlaySummary on the backend.

function hdrIcon(value: string | undefined): OverlayIconId | null {
  if (!value) return null;
  if (value.includes("DV")) return "dolby-vision";
  if (value.includes("HDR10")) return "hdr10";
  return "hdr";
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
    getValue: (d) => d.resolution?.toUpperCase() ?? null,
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
    getIcon: (d) => hdrIcon(d.hdr),
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
