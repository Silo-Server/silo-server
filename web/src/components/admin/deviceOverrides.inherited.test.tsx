import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { DeviceProfileTabs } from "@/components/admin/deviceOverrides";
import type { AdminDeviceSetting, AdminUserSettingEntry } from "@/hooks/queries/admin/users";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}

const device = {
  userId: 1,
  deviceId: "tv-1",
  deviceName: "Living Room TV",
  devicePlatform: "tvOS",
};

function override(key: string, value: string): AdminDeviceSetting {
  return {
    user_id: 1,
    profile_id: "alex",
    profile_name: "Alex",
    device_id: "tv-1",
    device_name: "Living Room TV",
    device_platform: "tvOS",
    key,
    value,
    updated_at: "2026-10-01T00:00:00Z",
  };
}

function renderTabs(
  settings: AdminDeviceSetting[],
  storedSettings: AdminUserSettingEntry[] | undefined,
) {
  render(
    <DeviceProfileTabs
      profiles={[{ profileId: "alex", profileName: "Alex", settings }]}
      showAllSettings
      storedSettings={storedSettings}
      device={device}
      onResetProfile={vi.fn()}
      onEditJson={vi.fn()}
      onResetSetting={vi.fn()}
      onChangeSetting={vi.fn()}
      updatePending={false}
    />,
  );
}

/** The row holding a setting, found by its raw key line. */
function row(key: string): HTMLElement {
  const keyLine = screen.getByText(key, { exact: true });
  return keyLine.parentElement!.parentElement as HTMLElement;
}

describe("DeviceProfileTabs inherited values", () => {
  it("shows the profile's value, not the app default, for a key the device doesn't override", () => {
    renderTabs(
      [],
      [{ key: "playback.audio_language", value: "fr", scope: "profile", profile_id: "alex" }],
    );

    const audio = row("playback.audio_language");
    expect(within(audio).getByText("profile")).toBeInTheDocument();
    expect(within(audio).getByText(/from Alex's profile/).parentElement).toHaveTextContent(
      "French · from Alex's profile",
    );
    expect(within(audio).getByRole("combobox")).toHaveTextContent("French");
    expect(within(audio).queryByText("default")).not.toBeInTheDocument();
  });

  it("labels a value nobody set as the app default", () => {
    renderTabs([], []);

    const audio = row("playback.audio_language");
    expect(within(audio).getByText("default")).toBeInTheDocument();
    expect(within(audio).getByText(/app default/)).toBeInTheDocument();
  });

  it("gives a device-only key the app default even when the profile has values", () => {
    renderTabs(
      [],
      [{ key: "playback.audio_language", value: "fr", scope: "profile", profile_id: "alex" }],
    );

    const hdr = row("player.hdr_enabled");
    expect(within(hdr).getByText("default")).toBeInTheDocument();
    expect(within(hdr).getByText(/app default/).parentElement).toHaveTextContent(
      "Enabled · app default",
    );
  });

  it("keeps showing the device's own value for an override", () => {
    renderTabs(
      [override("playback.audio_language", "de")],
      [
        { key: "playback.audio_language", value: "fr", scope: "profile", profile_id: "alex" },
        {
          key: "playback.audio_language",
          value: "de",
          scope: "profile_device",
          profile_id: "alex",
          device_id: "tv-1",
        },
      ],
    );

    const audio = row("playback.audio_language");
    expect(within(audio).getByRole("combobox")).toHaveTextContent("German");
    expect(within(audio).getByLabelText("overridden")).toBeInTheDocument();
    expect(within(audio).queryByText(/from Alex's profile/)).not.toBeInTheDocument();
  });

  it("falls back to app defaults while the stored values are still loading", () => {
    renderTabs([], undefined);

    expect(within(row("playback.audio_language")).getByText("default")).toBeInTheDocument();
  });
});
