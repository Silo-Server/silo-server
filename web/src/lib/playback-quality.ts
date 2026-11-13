import { t } from "@/i18n";

export type PlaybackQualityPreset = "any" | "standard" | "4k";

/** A resolution name, the same in every language. */
const QUALITY_4K = "4K";

export const PLAYBACK_QUALITY_OPTIONS: Array<{
  value: PlaybackQualityPreset;
  label: string;
  description: string;
}> = [
  {
    value: "any",
    get label() {
      return t("web.profiles.helpers.quality.any");
    },
    get description() {
      return t("web.profiles.helpers.quality.anyDescription");
    },
  },
  {
    value: "standard",
    get label() {
      return t("web.profiles.helpers.quality.standard");
    },
    get description() {
      return t("web.profiles.helpers.quality.standardDescription");
    },
  },
  {
    value: "4k",
    label: QUALITY_4K,
    get description() {
      return t("web.profiles.helpers.quality.fourKDescription");
    },
  },
];

export function canonicalPlaybackQuality(value: string | null | undefined): string {
  switch ((value ?? "").trim().toLowerCase()) {
    case "":
    case "any":
      return "";
    case "standard":
    case "480p":
    case "720p":
    case "1080p":
      return "1080p";
    case "4k":
    case "uhd":
    case "2160p":
    case "4320p":
      return "2160p";
    default:
      return "";
  }
}

export function playbackQualityPresetFromValue(
  value: string | null | undefined,
): PlaybackQualityPreset {
  switch (canonicalPlaybackQuality(value)) {
    case "2160p":
      return "4k";
    case "1080p":
      return "standard";
    default:
      return "any";
  }
}

export function playbackQualityValueFromPreset(preset: PlaybackQualityPreset): string {
  switch (preset) {
    case "standard":
      return "1080p";
    case "4k":
      return "2160p";
    default:
      return "";
  }
}

export function formatPlaybackQualityPreset(value: string | null | undefined): string {
  switch (playbackQualityPresetFromValue(value)) {
    case "standard":
      return t("web.profiles.helpers.quality.standard");
    case "4k":
      return QUALITY_4K;
    default:
      return t("web.profiles.helpers.quality.any");
  }
}
