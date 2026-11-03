import type { EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_DEFINITIONS, SETTING_KEYS } from "@/lib/settingsContract";

export interface PlaybackQualitySettings {
  /** Undefined until the settings read resolves. */
  qualityPreference: string | undefined;
  maxBitrateKbps: number | null | undefined;
}

/**
 * The resolution and bitrate limits playback starts with. On a metered link
 * (Data Saver or a cellular connection) the cellular pair replaces the main
 * one, so a phone browser on mobile data starts within the same data limit
 * the apps use instead of at the source's full bitrate. Playback can start
 * before the settings read resolves and does not replan when it does, and an
 * older server has no cellular pair to read, so a metered start without the
 * pair uses its contract defaults rather than no limit.
 */
export function playbackQualitySettings(
  effective: EffectiveSettingsMap | undefined,
  metered: boolean,
): PlaybackQualitySettings {
  const qualityKey = metered
    ? SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY
    : SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY;
  const bitrateKey = metered
    ? SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS
    : SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS;
  if (metered) {
    const quality = effective?.[qualityKey];
    const bitrate = effective?.[bitrateKey];
    return {
      qualityPreference: (quality
        ? quality.value
        : SETTING_DEFINITIONS[qualityKey].defaultValue) as string,
      maxBitrateKbps: (bitrate ? bitrate.value : SETTING_DEFINITIONS[bitrateKey].defaultValue) as
        | number
        | null,
    };
  }
  return {
    qualityPreference: effective?.[qualityKey]?.value as string | undefined,
    maxBitrateKbps: effective?.[bitrateKey]?.value as number | null | undefined,
  };
}
