import { describe, expect, it } from "vitest";

import type { UserDevice } from "@/api/types";
import { deviceRecencyGroup } from "@/hooks/queries/devices";
import { effectiveSettingsQueryKey } from "@/hooks/queries/settingValues";

const NOW = Date.parse("2026-07-31T12:00:00Z");

function device(overrides: Partial<UserDevice> = {}): UserDevice {
  return {
    device_id: "device-1",
    device_name: "Chrome",
    device_platform: "macOS Web",
    last_seen_at: new Date(NOW).toISOString(),
    profile_id: "profile-1",
    profile_name: "Sam",
    is_current_device: false,
    changed_count: 0,
    ...overrides,
  };
}

describe("deviceRecencyGroup", () => {
  it("puts the current device first regardless of its timestamp", () => {
    const stale = device({
      is_current_device: true,
      last_seen_at: new Date(NOW - 400 * 24 * 60 * 60 * 1000).toISOString(),
    });
    expect(deviceRecencyGroup(stale, NOW)).toBe("current");
  });

  it("splits the rest at a week", () => {
    const recent = device({ last_seen_at: new Date(NOW - 6 * 24 * 60 * 60 * 1000).toISOString() });
    const older = device({ last_seen_at: new Date(NOW - 8 * 24 * 60 * 60 * 1000).toISOString() });
    expect(deviceRecencyGroup(recent, NOW)).toBe("week");
    expect(deviceRecencyGroup(older, NOW)).toBe("earlier");
  });

  it("treats an unparseable timestamp as old rather than throwing", () => {
    expect(deviceRecencyGroup(device({ last_seen_at: "nonsense" }), NOW)).toBe("earlier");
  });
});

describe("effectiveSettingsQueryKey", () => {
  // Without device and profile in the key, reading the Apple TV's values would
  // land on the same cache entry as this browser's and serve one device's
  // settings as another's.
  it("gives each device its own cache entry", () => {
    const keys = ["player.hdr_enabled"] as const;
    const own = effectiveSettingsQueryKey({ keys });
    const appleTv = effectiveSettingsQueryKey({ keys, deviceId: "apple-tv" });
    const iPad = effectiveSettingsQueryKey({ keys, deviceId: "ipad" });

    expect(appleTv).not.toEqual(own);
    expect(appleTv).not.toEqual(iPad);
  });

  it("gives each profile its own cache entry", () => {
    const keys = ["player.hdr_enabled"] as const;
    const mine = effectiveSettingsQueryKey({ keys, deviceId: "shared-tv" });
    const theirs = effectiveSettingsQueryKey({
      keys,
      deviceId: "shared-tv",
      profileId: "profile-2",
    });

    expect(theirs).not.toEqual(mine);
  });

  it("is stable for the same request", () => {
    const keys = ["player.hdr_enabled"] as const;
    expect(effectiveSettingsQueryKey({ keys, deviceId: "tv" })).toEqual(
      effectiveSettingsQueryKey({ keys, deviceId: "tv" }),
    );
  });
});
