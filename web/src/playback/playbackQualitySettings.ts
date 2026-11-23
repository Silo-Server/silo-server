import type { EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";
import type { PlaybackNetworkTier } from "@/player/bandwidth-probe";

/** Bitrate ceiling applied on top of the cellular pair when Data Saver is on. */
export const LOW_DATA_CAP_KBPS = 2_000;

export interface PlaybackQualitySettings {
  /** Undefined until the settings read resolves. */
  qualityPreference: string | undefined;
  maxBitrateKbps: number | null | undefined;
}

/**
 * The resolution and bitrate limits playback starts with. On a metered link
 * (cellular or Data Saver) the cellular pair replaces the main one, so a phone
 * browser on mobile data starts within the same data limit the apps use instead
 * of at the source's full bitrate. When Data Saver is on (`low_data`), a 2 Mbps
 * bitrate ceiling applies on top of the cellular pair without raising a
 * stricter saved cap. Playback can start before the settings read resolves and
 * does not replan when it does, and an older server has no cellular pair to
 * read, so a metered start without the pair uses its contract defaults rather
 * than no limit.
 */
export function playbackQualitySettings(
  effective: EffectiveSettingsMap | undefined,
  tier: PlaybackNetworkTier,
): PlaybackQualitySettings {
  const metered = tier !== "unmetered";
  const qualityKey = metered
    ? SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY
    : SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY;
  const bitrateKey = metered
    ? SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS
    : SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS;
  if (metered) {
    const quality = effective?.[qualityKey];
    const bitrate = effective?.[bitrateKey];
    const baseCap = (bitrate ? bitrate.value : SETTING_DEFINITIONS[bitrateKey].defaultValue) as
      | number
      | null;
    let maxBitrateKbps = baseCap;
    if (tier === "low_data") {
      maxBitrateKbps =
        baseCap != null && baseCap > 0 ? Math.min(baseCap, LOW_DATA_CAP_KBPS) : LOW_DATA_CAP_KBPS;
    }
    return {
      qualityPreference: (quality
        ? quality.value
        : SETTING_DEFINITIONS[qualityKey].defaultValue) as string,
      maxBitrateKbps,
    };
  }
  return {
    qualityPreference: effective?.[qualityKey]?.value as string | undefined,
    maxBitrateKbps: effective?.[bitrateKey]?.value as number | null | undefined,
  };
}
