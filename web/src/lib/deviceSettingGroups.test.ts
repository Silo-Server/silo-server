import { describe, expect, it } from "vitest";

import {
  groupDeviceSettings,
  groupForDeviceSetting,
  hiddenDeviceSettingKeys,
  manifestPlatformFor,
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

  it("keeps appearance settings off the device screen", () => {
    expect(groupForDeviceSetting("ui.theme")).toBeNull();
    expect(groupForDeviceSetting("ui.library_page_state")).toBeNull();
  });

  it("returns groups in reading order and omits empty ones", () => {
    const ids = groupDeviceSettings().map((group) => group.id);
    expect(ids).toEqual(["picture", "sound", "subtitles", "episodes", "appearance"]);

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

  // The web player never reads these: speed is set in the player per session,
  // subtitle timing has no offset control, and HDR comes from what the browser
  // reports it can display. Offering them for a browser saves a value that
  // changes nothing.
  const NATIVE_ONLY_PLAYER_KEYS = [
    "player.playback_speed",
    "player.subtitle_sync_ms",
    "player.hdr_enabled",
    "player.audio_sync_ms",
  ] as const;

  function shownKeys(devicePlatform: string, stored: string[] = []) {
    return groupDeviceSettings(undefined, {
      devicePlatform,
      keysWithStoredValues: new Set(stored),
    }).flatMap((group) => group.keys as string[]);
  }

  it.each(["Windows Web", "macOS Web", "iOS Web", "Android Web", "Linux Web", "Web"])(
    "does not offer player settings the web player ignores on %s",
    (platform) => {
      const shown = shownKeys(platform);
      for (const key of NATIVE_ONLY_PLAYER_KEYS) expect(shown).not.toContain(key);
      // Settings the web player does honour stay.
      expect(shown).toContain("playback.audio_language");
      expect(shown).toContain("playback.auto_play_next");
    },
  );

  it.each(["tvOS", "iOS", "iPadOS", "macOS", "android", "android-tv"])(
    "still offers them on the native %s app",
    (platform) => {
      const shown = shownKeys(platform);
      for (const key of NATIVE_ONLY_PLAYER_KEYS) expect(shown).toContain(key);
    },
  );

  it("keeps a value a browser already stores visible so it can be cleared", () => {
    expect(shownKeys("Windows Web", ["player.playback_speed"])).toContain("player.playback_speed");
  });
});
