import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import {
  DeviceProfileTabs,
  deviceSettingKeysForPlatform,
} from "@/components/admin/deviceOverrides";
import type { AdminDeviceSetting } from "@/hooks/queries/admin/users";

// Radix Slider and Select read element sizes via ResizeObserver, which jsdom
// does not provide.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}

const NATIVE_ONLY_PLAYER_KEYS = [
  "player.playback_speed",
  "player.subtitle_sync_ms",
  "player.hdr_enabled",
];

function renderAllSettings(devicePlatform: string, settings: AdminDeviceSetting[] = []) {
  render(
    <DeviceProfileTabs
      profiles={[{ profileId: "p1", profileName: "Alex", settings }]}
      showAllSettings
      device={{ userId: 1, deviceId: "d1", deviceName: "Device", devicePlatform }}
      onResetProfile={vi.fn()}
      onEditJson={vi.fn()}
      onResetSetting={vi.fn()}
      onChangeSetting={vi.fn()}
      updatePending={false}
    />,
  );
}

describe("DeviceProfileTabs platform filtering", () => {
  it("does not offer player settings the web player ignores for a browser", () => {
    renderAllSettings("Windows Web");

    for (const key of NATIVE_ONLY_PLAYER_KEYS) {
      expect(screen.queryByText(key)).not.toBeInTheDocument();
    }
    expect(screen.getByText("playback.audio_language")).toBeInTheDocument();
  });

  it("offers them for a native TV app", () => {
    renderAllSettings("tvOS");

    for (const key of NATIVE_ONLY_PLAYER_KEYS) {
      expect(screen.getByText(key)).toBeInTheDocument();
    }
  });

  it("still lists an override a browser already stores so it can be reset", () => {
    renderAllSettings("Windows Web", [
      {
        user_id: 1,
        profile_id: "p1",
        profile_name: "Alex",
        device_id: "d1",
        device_name: "Device",
        device_platform: "Windows Web",
        key: "player.playback_speed",
        value: "1.5",
        updated_at: "2026-10-01T00:00:00Z",
      },
    ]);

    expect(screen.getByText("player.playback_speed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reset" })).toBeInTheDocument();
  });

  it("counts the settings a device is offered, including stored inapplicable ones", () => {
    const web = deviceSettingKeysForPlatform("Windows Web");
    const tv = deviceSettingKeysForPlatform("tvOS");
    for (const key of NATIVE_ONLY_PLAYER_KEYS) {
      expect(web).not.toContain(key);
      expect(tv).toContain(key);
    }
    expect(deviceSettingKeysForPlatform("Windows Web", ["player.playback_speed"])).toHaveLength(
      web.length + 1,
    );
  });
});
