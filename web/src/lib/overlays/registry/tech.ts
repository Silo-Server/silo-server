import { compactHdrSuffix, prettyResolution } from "@/lib/mediaFormat";
import { t } from "@/i18n";
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
    get label() {
      return t("web.settings.overlays.badge.resolution.label");
    },
    get description() {
      return t("web.settings.overlays.badge.resolution.description");
    },
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
    get label() {
      return t("web.settings.overlays.badge.hdr.label");
    },
    get description() {
      return t("web.settings.overlays.badge.hdr.description");
    },
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
    get label() {
      return t("web.settings.overlays.badge.resolutionHdr.label");
    },
    get shortLabel() {
      return t("web.settings.overlays.badge.resolutionHdr.shortLabel");
    },
    get description() {
      return t("web.settings.overlays.badge.resolutionHdr.description");
    },
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
    id: "resolution_hdr_audio",
    category: "tech",
    get label() {
      return t("web.settings.overlays.badge.resolutionHdrAudio.label");
    },
    get shortLabel() {
      return t("web.settings.overlays.badge.resolutionHdrAudio.shortLabel");
    },
    get description() {
      return t("web.settings.overlays.badge.resolutionHdrAudio.description");
    },
    defaultPosition: "top-left",
    defaultEnabled: false,
    iconCapable: true,
    introducedInManifest: 26,
    // The parts the file has, so a file without a known resolution still
    // shows its audio codec while the standalone audio badge is hidden.
    getValue: (d) =>
      [prettyResolution(d.resolution), compactHdrSuffix(d.hdr), d.audio]
        .filter(Boolean)
        .join(" ") || null,
    // Same rule as the Resolution + HDR badge: an Atmos or HDR10 wordmark
    // would repeat words already in the label.
    getIcon: (d) => (d.hdr?.includes("DV") ? "dolby-vision" : null),
    // The API's resolution, dynamic range and audio values joined by a space:
    // "2160p DV HDR10 Atmos", or only the parts the file has.
    getValueKey: (d) => [d.resolution, d.hdr, d.audio].filter(Boolean).join(" ") || null,
  },
  {
    id: "audio",
    category: "tech",
    get label() {
      return t("web.settings.overlays.badge.audio.label");
    },
    get description() {
      return t("web.settings.overlays.badge.audio.description");
    },
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
    get label() {
      return t("web.settings.overlays.badge.audioChannels.label");
    },
    get description() {
      return t("web.settings.overlays.badge.audioChannels.description");
    },
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
    get label() {
      return t("web.settings.overlays.badge.videoCodec.label");
    },
    get description() {
      return t("web.settings.overlays.badge.videoCodec.description");
    },
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
    get label() {
      return t("web.settings.overlays.badge.container.label");
    },
    get description() {
      return t("web.settings.overlays.badge.container.description");
    },
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconCapable: false,
    getValue: (d) => d.container ?? null,
    getValueKey: (d) => d.container || null,
  },
  {
    id: "aspect_ratio",
    category: "tech",
    get label() {
      return t("web.settings.overlays.badge.aspectRatio.label");
    },
    get description() {
      return t("web.settings.overlays.badge.aspectRatio.description");
    },
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
    get label() {
      return t("web.settings.overlays.badge.releaseType.label");
    },
    get description() {
      return t("web.settings.overlays.badge.releaseType.description");
    },
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
    get label() {
      return t("web.settings.overlays.badge.edition.label");
    },
    get description() {
      return t("web.settings.overlays.badge.edition.description");
    },
    defaultPosition: "bottom-left",
    defaultEnabled: false,
    iconCapable: false,
    getValue: (d) => d.edition ?? null,
    getValueKey: (d) => d.edition || null,
  },
  {
    id: "multi_audio",
    category: "tech",
    get label() {
      return t("web.settings.overlays.badge.multiAudio.label");
    },
    get description() {
      return t("web.settings.overlays.badge.multiAudio.description");
    },
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "languages",
    iconCapable: true,
    getValue: (d) => (d.multi_audio ? t("web.settings.overlays.badge.multiAudio.value") : null),
  },
  {
    id: "multi_sub",
    category: "tech",
    get label() {
      return t("web.settings.overlays.badge.multiSub.label");
    },
    get description() {
      return t("web.settings.overlays.badge.multiSub.description");
    },
    defaultPosition: "bottom-right",
    defaultEnabled: false,
    iconId: "subtitles",
    iconCapable: true,
    getValue: (d) => (d.multi_sub ? "CC" : null),
  },
];
