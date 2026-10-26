import { describe, expect, it } from "vitest";
import type { EffectiveSetting, EffectiveSettingsMap } from "@/hooks/queries/settingValues";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { playbackQualitySettings } from "./playbackQualitySettings";

function setting(value: unknown): EffectiveSetting {
  return { value, source: "default" } as EffectiveSetting;
}

const effective: EffectiveSettingsMap = {
  [SETTING_KEYS.PLAYBACK_PREFERRED_QUALITY]: setting("auto"),
  [SETTING_KEYS.PLAYBACK_MAX_BITRATE_KBPS]: setting(null),
  [SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY]: setting("720p"),
  [SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS]: setting(2000),
};

describe("playbackQualitySettings", () => {
  it("uses the main pair on an unmetered link", () => {
    expect(playbackQualitySettings(effective, false)).toEqual({
      qualityPreference: "auto",
      maxBitrateKbps: null,
    });
  });

  it("uses the cellular pair on a metered link", () => {
    expect(playbackQualitySettings(effective, true)).toEqual({
      qualityPreference: "720p",
      maxBitrateKbps: 2000,
    });
  });

  it("leaves both unset until the settings read resolves on an unmetered link", () => {
    expect(playbackQualitySettings(undefined, false)).toEqual({
      qualityPreference: undefined,
      maxBitrateKbps: undefined,
    });
  });

  it("keeps a stored no-limit cellular bitrate", () => {
    expect(
      playbackQualitySettings(
        { ...effective, [SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS]: setting(null) },
        true,
      ),
    ).toEqual({ qualityPreference: "720p", maxBitrateKbps: null });
  });

  // An older server defines no cellular pair, so the read never returns it.
  it("uses the cellular defaults on a metered link when the server has no cellular pair", () => {
    const {
      [SETTING_KEYS.PLAYBACK_CELLULAR_PREFERRED_QUALITY]: _quality,
      [SETTING_KEYS.PLAYBACK_CELLULAR_MAX_BITRATE_KBPS]: _bitrate,
      ...withoutCellular
    } = effective;
    expect(playbackQualitySettings(withoutCellular, true)).toEqual({
      qualityPreference: "720p",
      maxBitrateKbps: 2000,
    });
  });

  // Playback can start before the read resolves and does not replan when it
  // does, so a metered start must not go out without a limit.
  it("uses the cellular defaults until the settings read resolves on a metered link", () => {
    expect(playbackQualitySettings(undefined, true)).toEqual({
      qualityPreference: "720p",
      maxBitrateKbps: 2000,
    });
  });
});
