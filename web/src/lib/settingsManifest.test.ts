import { describe, expect, it } from "vitest";

import { ALL_DEVICE_SETTING_KEYS, getSettingDefinition } from "./settingsManifest";

describe("settingsManifest", () => {
  it("keeps the client-consumed credits override editable", () => {
    expect(ALL_DEVICE_SETTING_KEYS).toContain("playback.auto_skip_credits");
    expect(getSettingDefinition("playback.auto_skip_credits")).toMatchObject({
      scope: "device",
      control: "switch",
      defaultValue: "false",
    });
  });

  it("does not expose inert profile-only playback fields as device overrides", () => {
    expect(ALL_DEVICE_SETTING_KEYS).not.toContain("playback.auto_skip_recap");
    expect(ALL_DEVICE_SETTING_KEYS).not.toContain("playback.auto_play_next_preview");
  });
});
