// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  refetchCapabilities: vi.fn(),
  useEffectiveSettings: vi.fn(),
  capabilities: {
    data: undefined as undefined | { revision: number },
    isLoading: false,
    isError: true,
    isFetching: false,
  },
}));

vi.mock("@/hooks/queries/devices", () => ({
  useMyDevices: () => ({
    data: [
      {
        device_id: "living-room",
        device_name: "Living Room TV",
        device_platform: "tvOS",
        last_seen_at: "2026-08-04T00:00:00Z",
        profile_id: "profile-1",
        profile_name: "Taylor",
        is_current_device: true,
        changed_count: 0,
      },
    ],
    isLoading: false,
  }),
  useClearDeviceSettings: () => ({ mutate: vi.fn(), isPending: false }),
  useForgetDevice: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/queries/settingValues", () => ({
  useSettingsCapabilities: () => ({
    ...mocks.capabilities,
    refetch: mocks.refetchCapabilities,
  }),
  useEffectiveSettings: (...args: unknown[]) => mocks.useEffectiveSettings(...args),
  useSetSettingValue: () => ({ mutate: vi.fn(), isPending: false }),
  useClearSettingValue: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "profile-1", is_primary: false } }),
}));

vi.mock("@/hooks/useIsActingAdmin", () => ({
  useIsActingAdmin: () => false,
}));

vi.mock("@/components/settings/DeviceList", () => ({
  DeviceList: () => <div>Device list</div>,
  lastSeenLabel: () => "recently",
}));

vi.mock("@/components/settings/DeviceSettingGroups", () => ({
  DeviceSettingGroups: () => <div>Editable device defaults</div>,
}));

vi.mock("@/components/settings/SubtitleAppearancePanelView", () => ({
  SubtitleAppearancePanelView: () => null,
}));

import DeviceSettings from "./DeviceSettings";

describe("DeviceSettings capability discovery", () => {
  beforeEach(() => {
    mocks.refetchCapabilities.mockReset();
    mocks.useEffectiveSettings.mockReset();
    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.capabilities.data = undefined;
    mocks.capabilities.isLoading = false;
    mocks.capabilities.isError = true;
    mocks.capabilities.isFetching = false;
  });

  it("fails closed and offers a retry when capabilities cannot be loaded", async () => {
    const user = userEvent.setup();
    render(<DeviceSettings />);

    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        keys: [],
        deviceId: "living-room",
        enabled: false,
      }),
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Device controls stay unavailable until Silo confirms which settings this server supports.",
    );
    expect(screen.queryByText("Editable device defaults")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Retry compatibility check" }));
    expect(mocks.refetchCapabilities).toHaveBeenCalledTimes(1);
  });
});
