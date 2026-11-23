import { describe, expect, it } from "vitest";
import type { EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_KEYS, type SettingKey } from "@/lib/settingsContract";
import { LOW_DATA_CAP_KBPS, playbackQualitySettings } from "./playbackQualitySettings";

function effectiveEntry<K extends SettingKey>(key: K, value: unknown): EffectiveSettingsMap[K] {
  return {
    key,
    value,
    scope: "profile",
    source: "profile",
    constrained: false,
  } as EffectiveSettingsMap[K];
}

describe("playbackQualitySettings", () => {
  it("defaults metered links to Auto capped at 10 Mbps before settings resolve", () => {
    expect(playbackQualitySettings(undefined, "metered")).toEqual({
      qualityPreference: "auto",
      maxBitrateKbps: 10_000,
    });
  });

  it("caps low_data links to 2 Mbps without changing resolution", () => {
    expect(playbackQualitySettings(undefined, "low_data")).toEqual({
      qualityPreference: "auto",
      maxBitrateKbps: LOW_DATA_CAP_KBPS,
    });

    const uncappedCellular: EffectiveSettingsMap = {
      [SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY]: effectiveEntry(
        SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY,
        "auto",
      ),
      [SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS]: effectiveEntry(
        SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS,
        null,
      ),
    };
    expect(playbackQualitySettings(uncappedCellular, "metered")).toEqual({
      qualityPreference: "auto",
      maxBitrateKbps: null,
    });
    expect(playbackQualitySettings(uncappedCellular, "low_data")).toEqual({
      qualityPreference: "auto",
      maxBitrateKbps: LOW_DATA_CAP_KBPS,
    });
  });

  it("never raises a stricter saved cellular override under low_data", () => {
    const strictCellular: EffectiveSettingsMap = {
      [SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY]: effectiveEntry(
        SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY,
        "480p",
      ),
      [SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS]: effectiveEntry(
        SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS,
        1_500,
      ),
    };
    expect(playbackQualitySettings(strictCellular, "low_data")).toEqual({
      qualityPreference: "480p",
      maxBitrateKbps: 1_500,
    });
  });

  it("uses the main pair on unmetered links", () => {
    const settings: EffectiveSettingsMap = {
      [SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY]: effectiveEntry(
        SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY,
        "2160p",
      ),
      [SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS]: effectiveEntry(
        SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS,
        40_000,
      ),
      [SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY]: effectiveEntry(
        SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY,
        "720p",
      ),
      [SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS]: effectiveEntry(
        SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS,
        2_000,
      ),
    };
    expect(playbackQualitySettings(settings, "unmetered")).toEqual({
      qualityPreference: "2160p",
      maxBitrateKbps: 40_000,
    });
  });
});
