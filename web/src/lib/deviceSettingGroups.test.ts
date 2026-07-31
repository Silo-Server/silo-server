import { describe, expect, it } from "vitest";

import {
  groupDeviceSettings,
  groupForDeviceSetting,
  hiddenDeviceSettingKeys,
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
});
