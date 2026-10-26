import { describe, expect, it } from "vitest";

import type { AdminUserDeviceRow } from "@/api/v2/adminUserActivity";
import type { AdminUserSettingEntry } from "@/hooks/queries/admin/users";
import { SETTING_KEYS } from "@/lib/settingsContract";

import { addableSettings, buildPreferenceLevels, replacedValue, replacesText } from "./levels";

const profiles = [
  { id: "p1", name: "Main" },
  { id: "p2", name: "Kids" },
];

function device(id: string, name: string, seenBy: string[], platform = "tvOS"): AdminUserDeviceRow {
  return {
    device_id: id,
    device_name: name,
    device_platform: platform,
    last_seen_at: "2026-09-30T10:00:00Z",
    last_updated: null,
    override_count: 0,
    profiles: seenBy.map((profileId) => ({
      profile_id: profileId,
      profile_name: profileId,
      override_count: 0,
      last_seen_at: `2026-09-2${seenBy.indexOf(profileId)}T10:00:00Z`,
    })),
  };
}

const entries: AdminUserSettingEntry[] = [
  { key: SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, scope: "profile", profile_id: "p1", value: "off" },
  {
    key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP,
    scope: "profile",
    profile_id: "p1",
    value: "true",
  },
  {
    key: SETTING_KEYS.PLAYBACK_SUBTITLE_MODE,
    scope: "profile_device",
    profile_id: "p1",
    device_id: "shield",
    value: "always",
  },
  {
    key: SETTING_KEYS.PLAYER_AUDIO_SYNC_MS,
    scope: "profile_device",
    profile_id: "p1",
    device_id: "gone",
    value: "40",
  },
  {
    key: SETTING_KEYS.PLAYBACK_SUBTITLE_MODE,
    scope: "profile_library",
    profile_id: "p1",
    library_id: 3,
    value: "always",
  },
  {
    key: SETTING_KEYS.PLAYBACK_SUBTITLE_MODE,
    scope: "profile_series",
    profile_id: "p1",
    series_id: "s-1883",
    value: "auto",
  },
  {
    key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP,
    scope: "profile",
    profile_id: "p-removed",
    value: "true",
  },
];

function build() {
  return buildPreferenceLevels({
    entries,
    profiles,
    devices: [device("atv", "Apple TV 4K", ["p2"]), device("shield", "Shield TV", [])],
    libraryNames: new Map([[3, "TV Shows"]]),
  });
}

describe("buildPreferenceLevels", () => {
  it("groups levels by profile in profile order with their counts", () => {
    const groups = build();
    expect(groups.map((group) => group.title)).toEqual([
      "Profile · Main",
      "Profile · Kids",
      "Profile · p-removed",
    ]);
    const summary = (index: number) =>
      groups[index]!.levels.map((level) => [level.id, level.name, level.entries.length]);
    expect(summary(0)).toEqual([
      ["profile.p1", "All devices", 2],
      // Listed because Main has settings on it, though it was never seen with Main.
      ["device.p1.shield", "Shield TV", 1],
      // Settings on a device the account no longer lists keep their level.
      ["device.p1.gone", "Device gone", 1],
      ["library.p1.3", "TV Shows library", 1],
      ["series.p1.s-1883", "Series s-1883", 1],
    ]);
    // Kids has nothing stored, yet keeps its levels: All devices and the
    // device it was seen on.
    expect(summary(1)).toEqual([
      ["profile.p2", "All devices", 0],
      ["device.p2.atv", "Apple TV 4K", 0],
    ]);
    expect(groups[1]!.levels[1]!.device).toEqual({
      id: "atv",
      platform: "tvOS",
      lastSeenAt: "2026-09-20T10:00:00Z",
    });
  });

  it("gives each level the identity a new setting is written at", () => {
    const [main] = build();
    expect(main!.levels.map((level) => level.identity)).toEqual([
      { scope: "profile", profileId: "p1" },
      { scope: "profile_device", profileId: "p1", deviceId: "shield" },
      { scope: "profile_device", profileId: "p1", deviceId: "gone" },
      { scope: "profile_library", profileId: "p1", libraryId: 3 },
      { scope: "profile_series", profileId: "p1", seriesId: "s-1883" },
    ]);
  });

  it("puts account-scoped rows on their own level first", () => {
    const groups = buildPreferenceLevels({
      entries: [{ key: "legacy.flag", scope: "account", value: "1" }],
      profiles,
      devices: [],
      libraryNames: new Map(),
    });
    expect(groups[0]!.levels.map((level) => [level.id, level.entries.length])).toEqual([
      ["account", 1],
    ]);
  });
});

describe("replaced values", () => {
  it("says when an app-family, device, or library value can come in between", () => {
    const [main] = build();
    const [profile, shield, , library, series] = main!.levels;
    const all = main!.levels.flatMap((level) => level.entries);
    // A library sits above devices, and Shield stores its own subtitle mode.
    expect(
      replacesText(
        replacedValue(library!, SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, profile!.entries, all),
      ),
    ).toBe("Replaces Main: Off, or a device setting where one applies");
    // A series sits above libraries; the first such layer is named.
    expect(
      replacedValue(series!, SETTING_KEYS.PLAYBACK_SUBTITLE_MODE, profile!.entries, all).orVaries,
    ).toBe("library");
    // A device level resolves before app families, which the page can't map.
    const family: AdminUserSettingEntry = {
      key: SETTING_KEYS.UI_CARD_PRESENTATION,
      scope: "profile_client",
      profile_id: "p1",
      client_family: "tv",
      value: "{}",
    };
    const withFamily = replacedValue(shield!, SETTING_KEYS.UI_CARD_PRESENTATION, profile!.entries, [
      ...all,
      family,
    ]);
    expect(withFamily.orVaries).toBe("app family");
    expect(
      replacedValue(shield!, SETTING_KEYS.UI_CARD_PRESENTATION, profile!.entries, all).orVaries,
    ).toBeUndefined();
  });

  it("offers only settings the level's scope allows, marking ones already set", () => {
    const [main] = build();
    const [profile, shield, , library] = main!.levels;
    const keys = (level: typeof shield) =>
      addableSettings(level!, profile!.entries).flatMap((group) =>
        group.settings.map((setting) => setting.key),
      );
    expect(keys(shield)).toContain(SETTING_KEYS.PLAYER_AUDIO_SYNC_MS);
    expect(keys(profile)).not.toContain(SETTING_KEYS.PLAYER_AUDIO_SYNC_MS);
    // Deprecated definitions are never offered.
    expect(keys(profile)).not.toContain(SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO);
    expect(keys(library)).toContain(SETTING_KEYS.PLAYBACK_SUBTITLE_MODE);
    expect(keys(library)).not.toContain(SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP);

    const subtitles = addableSettings(shield!, profile!.entries)
      .flatMap((group) => group.settings)
      .find((setting) => setting.key === SETTING_KEYS.PLAYBACK_SUBTITLE_MODE);
    expect(subtitles).toMatchObject({ category: "Playback", setHere: true });
  });
});
