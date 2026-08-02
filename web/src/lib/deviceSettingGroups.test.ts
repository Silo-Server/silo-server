import { describe, expect, it } from "vitest";

import {
  groupDeviceSettings,
  groupForDeviceSetting,
  hiddenDeviceSettingKeys,
  manifestPlatformFor,
  settingAppliesToPlatform,
} from "@/lib/deviceSettingGroups";
import { ALL_DEVICE_SETTING_KEYS } from "@/lib/settingsDisplay";

describe("deviceSettingGroups", () => {
  // The guard that matters: a key added to the manifest must either land in a
  // group or be deliberately hidden. Without this, a new device setting simply
  // never appears on the screen and nobody notices.
  it("places every device-scoped key in exactly one group or hides it deliberately", () => {
    const grouped = new Map<string, string[]>();
    for (const group of groupDeviceSettings()) {
      for (const key of group.keys) {
        grouped.set(key, [...(grouped.get(key) ?? []), group.id]);
      }
    }
    const hidden = new Set(hiddenDeviceSettingKeys());

    const unplaced = ALL_DEVICE_SETTING_KEYS.filter((key) => !grouped.has(key) && !hidden.has(key));
    expect(unplaced).toEqual([]);

    const duplicated = [...grouped.entries()].filter(([, groups]) => groups.length > 1);
    expect(duplicated).toEqual([]);
  });

  it("puts each setting where someone would look for it", () => {
    expect(groupForDeviceSetting("player.hdr_enabled")).toBe("picture");
    expect(groupForDeviceSetting("player.audio_sync_ms")).toBe("sound");
    expect(groupForDeviceSetting("playback.audio_language")).toBe("sound");
    expect(groupForDeviceSetting("playback.subtitle_mode")).toBe("subtitles");
    expect(groupForDeviceSetting("player.subtitle_sync_ms")).toBe("subtitles");
    expect(groupForDeviceSetting("playback.auto_play_next")).toBe("episodes");
  });

  it("keeps appearance settings off the device screen", () => {
    expect(groupForDeviceSetting("ui.theme")).toBeNull();
    expect(groupForDeviceSetting("ui.library_page_state")).toBeNull();
  });

  it("returns groups in reading order and omits empty ones", () => {
    const ids = groupDeviceSettings().map((group) => group.id);
    expect(ids).toEqual(["picture", "sound", "subtitles", "episodes"]);

    const single = groupDeviceSettings(["player.hdr_enabled"]);
    expect(single.map((group) => group.id)).toEqual(["picture"]);
  });

  // Browser platform strings all end in "Web" ("iOS Web"), so a phone browser
  // must classify as web, not as the phone's native app.
  it("maps self-reported platform strings onto the manifest's identifiers", () => {
    expect(manifestPlatformFor("macOS Web")).toBe("web");
    expect(manifestPlatformFor("iOS Web")).toBe("web");
    expect(manifestPlatformFor("iOS")).toBe("ios");
    expect(manifestPlatformFor("tvOS")).toBe("tvos");
    expect(manifestPlatformFor("android")).toBe("android");
    expect(manifestPlatformFor("android-tv")).toBe("android_tv");
    expect(manifestPlatformFor("Roku")).toBeNull();
    expect(manifestPlatformFor(undefined)).toBeNull();
  });

  it("hides settings the manifest marks as not applying to the device", () => {
    // Screen orientation is ios/android only; audio sync is native-only.
    expect(settingAppliesToPlatform("player.orientation_mode", "web")).toBe(false);
    expect(settingAppliesToPlatform("player.orientation_mode", "ios")).toBe(true);
    expect(settingAppliesToPlatform("player.audio_sync_ms", "web")).toBe(false);
    expect(settingAppliesToPlatform("player.audio_sync_ms", "tvos")).toBe(true);
    // An untagged setting is expected everywhere.
    expect(settingAppliesToPlatform("playback.subtitle_mode", "web")).toBe(true);
    // An unrecognized platform hides nothing.
    expect(settingAppliesToPlatform("player.orientation_mode", null)).toBe(true);

    const keys = groupDeviceSettings(undefined, { devicePlatform: "macOS Web" }).flatMap(
      (group) => group.keys,
    );
    expect(keys).not.toContain("player.orientation_mode");
    expect(keys).not.toContain("player.match_frame_rate");
    expect(keys).not.toContain("player.audio_sync_ms");
    expect(keys).toContain("playback.subtitle_mode");
  });

  // A stale override on a now-inapplicable setting must stay visible, or it
  // can never be cleared from this screen.
  it("keeps a platform-hidden setting visible while the device stores a value", () => {
    const keys = groupDeviceSettings(undefined, {
      devicePlatform: "macOS Web",
      keysWithStoredValues: new Set(["player.audio_sync_ms"]),
    }).flatMap((group) => group.keys);
    expect(keys).toContain("player.audio_sync_ms");
    expect(keys).not.toContain("player.orientation_mode");
  });
});
