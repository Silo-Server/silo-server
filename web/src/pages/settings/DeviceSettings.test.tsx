// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { UserDevice } from "@/api/types";
import type { SettingsCapabilities } from "@/hooks/queries/settingValues";

function device(overrides: Partial<UserDevice>): UserDevice {
  return {
    device_id: "living-room",
    device_name: "Living Room TV",
    device_platform: "tvOS",
    last_seen_at: "2026-08-04T00:00:00Z",
    profile_id: "profile-1",
    profile_name: "Taylor",
    is_current_device: true,
    changed_count: 0,
    ...overrides,
  };
}

const mocks = vi.hoisted(() => ({
  ownDevices: [] as UserDevice[],
  householdDevices: [] as UserDevice[],
  profile: { id: "profile-1", is_primary: false },
  refetchCapabilities: vi.fn(),
  useEffectiveSettings: vi.fn(),
  useStoredSettingValues: vi.fn(),
  deviceSettingGroupsProps: vi.fn(),
  clearDevice: vi.fn(),
  toastSuccess: vi.fn(),
  isActingAdmin: false,
  capabilities: {
    data: undefined as SettingsCapabilities | undefined,
    isLoading: false,
    isError: true,
    isFetching: false,
  },
}));

vi.mock("@/hooks/queries/devices", () => ({
  useMyDevices: (options?: { household?: boolean }) => ({
    data: options?.household ? mocks.householdDevices : mocks.ownDevices,
    isLoading: false,
  }),
  useClearDeviceSettings: () => ({ mutate: mocks.clearDevice, isPending: false }),
  useForgetDevice: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("sonner", () => ({
  toast: { success: (...args: unknown[]) => mocks.toastSuccess(...args), error: vi.fn() },
}));

vi.mock("@/hooks/queries/settingValues", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/queries/settingValues")>();
  return {
    ...actual,
    useSettingsCapabilities: () => ({
      ...mocks.capabilities,
      refetch: mocks.refetchCapabilities,
    }),
    useEffectiveSettings: (...args: unknown[]) => mocks.useEffectiveSettings(...args),
    useStoredSettingValues: (...args: unknown[]) => mocks.useStoredSettingValues(...args),
    useSetSettingValue: () => ({ mutate: vi.fn(), isPending: false }),
    useClearSettingValue: () => ({ mutate: vi.fn(), isPending: false }),
  };
});

vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: mocks.profile }),
}));

vi.mock("@/hooks/useIsActingAdmin", () => ({
  useIsActingAdmin: () => mocks.isActingAdmin,
}));

vi.mock("@/components/settings/DeviceList", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/components/settings/DeviceList")>()),
  DeviceList: () => <div>Device list</div>,
  lastSeenLabel: () => "recently",
}));

vi.mock("@/components/settings/DeviceSettingGroups", () => ({
  DeviceSettingGroups: (props: unknown) => {
    mocks.deviceSettingGroupsProps(props);
    return <div>Editable device defaults</div>;
  },
}));

vi.mock("@/components/settings/SubtitleAppearancePanelView", () => ({
  SubtitleAppearancePanelView: () => null,
}));

import DeviceSettings from "./DeviceSettings";

describe("DeviceSettings capability discovery", () => {
  const compatibleCapabilities: SettingsCapabilities = {
    api_version: 1,
    manifest_revision: 5,
    contract_etag: "revision-five",
    supports_batched_effective: true,
    supports_idempotent_writes: true,
  };

  beforeEach(() => {
    mocks.ownDevices = [device({})];
    mocks.householdDevices = [device({})];
    mocks.profile = { id: "profile-1", is_primary: false };
    mocks.refetchCapabilities.mockReset();
    mocks.useEffectiveSettings.mockReset();
    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useStoredSettingValues.mockReset();
    mocks.useStoredSettingValues.mockReturnValue({ data: undefined });
    mocks.deviceSettingGroupsProps.mockReset();
    mocks.clearDevice.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.isActingAdmin = false;
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

  it.each([
    ["API version is incompatible", { ...compatibleCapabilities, api_version: 2 }],
    [
      "batched effective reads are missing",
      { ...compatibleCapabilities, supports_batched_effective: undefined },
    ],
    ["revision is missing", { ...compatibleCapabilities, manifest_revision: undefined }],
  ])("does not request all settings when the %s", (_case, capabilities) => {
    mocks.capabilities.data = capabilities as SettingsCapabilities;

    render(<DeviceSettings />);

    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith(
      expect.objectContaining({ keys: [], enabled: false }),
    );
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.queryByText("Editable device defaults")).not.toBeInTheDocument();
  });

  it("still requests settings when the server reports no idempotent writes", () => {
    // The v2 write operations carry no mutation id, so replay support is not
    // a precondition for reading or editing device defaults.
    mocks.capabilities.data = {
      ...compatibleCapabilities,
      supports_idempotent_writes: undefined,
    } as SettingsCapabilities;

    render(<DeviceSettings />);

    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith(
      expect.objectContaining({ enabled: true }),
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("enables only revision-supported keys when the full capability contract matches", () => {
    mocks.capabilities.data = compatibleCapabilities;

    render(<DeviceSettings />);

    expect(mocks.useEffectiveSettings).toHaveBeenCalledWith(
      expect.objectContaining({
        keys: expect.arrayContaining(["player.hdr_enabled", "ui.card_presentation"]),
        enabled: true,
      }),
    );
    expect(screen.getByText("Editable device defaults")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("reads the device's own row for a key whose profile value is winning", () => {
    mocks.capabilities.data = { ...compatibleCapabilities, manifest_revision: 16 };
    mocks.useEffectiveSettings.mockReturnValue({
      data: {
        "ui.title_art": { key: "ui.title_art", value: true, source: "profile", scope: "profile" },
        "player.hdr_enabled": {
          key: "player.hdr_enabled",
          value: true,
          source: "profile",
          scope: "profile",
        },
      },
      isLoading: false,
    });
    mocks.useStoredSettingValues.mockReturnValue({ data: { "ui.title_art": false } });

    render(<DeviceSettings />);

    // Only ui.title_art resolves its profile value ahead of the device's own;
    // other keys' effective answers already name any device row.
    expect(mocks.useStoredSettingValues).toHaveBeenCalledWith(
      expect.objectContaining({
        keys: ["ui.title_art"],
        identity: expect.objectContaining({ scope: "profile_device", deviceId: "living-room" }),
        enabled: true,
      }),
    );
    expect(mocks.deviceSettingGroupsProps).toHaveBeenLastCalledWith(
      expect.objectContaining({ storedOnDevice: { "ui.title_art": false } }),
    );
  });

  describe("using profile settings on a device", () => {
    beforeEach(() => {
      mocks.capabilities.data = compatibleCapabilities;
      vi.spyOn(window, "confirm").mockReturnValue(true);
    });

    it("confirms in plain words and clears the device's own values", async () => {
      const user = userEvent.setup();
      mocks.ownDevices = [device({ changed_count: 3 })];
      mocks.clearDevice.mockImplementation((_vars, options: { onSuccess?: () => void }) =>
        options.onSuccess?.(),
      );

      render(<DeviceSettings />);

      expect(screen.queryByRole("button", { name: "Clear all changes" })).not.toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Use profile settings" }));

      expect(window.confirm).toHaveBeenCalledWith(
        "Use your profile settings on Living Room TV? This removes the 3 settings changed on this device. Settings that only apply to devices go back to the app default.",
      );
      expect(mocks.clearDevice).toHaveBeenCalledWith(
        { deviceId: "living-room", profileId: undefined },
        expect.anything(),
      );
      expect(mocks.toastSuccess).toHaveBeenCalledWith("Removed the changes on this device");
    });

    it("names the other profile and the singular when acting for someone else", async () => {
      const user = userEvent.setup();
      mocks.isActingAdmin = true;
      const samsIpad = device({
        device_name: "Sam's iPad",
        device_platform: "iPadOS",
        profile_id: "profile-2",
        profile_name: "Sam",
        is_current_device: false,
        changed_count: 1,
      });
      mocks.ownDevices = [samsIpad];
      mocks.householdDevices = [samsIpad];

      render(<DeviceSettings />);

      await user.click(screen.getByRole("button", { name: "Use profile settings" }));

      expect(window.confirm).toHaveBeenCalledWith(
        "Use Sam's profile settings on Sam's iPad? This removes the 1 setting changed on this device. Settings that only apply to devices go back to the app default.",
      );
      expect(mocks.clearDevice).toHaveBeenCalledWith(
        { deviceId: "living-room", profileId: "profile-2" },
        expect.anything(),
      );
    });

    it("keeps everything when the confirmation is declined", async () => {
      const user = userEvent.setup();
      vi.mocked(window.confirm).mockReturnValue(false);
      mocks.ownDevices = [device({ changed_count: 2 })];

      render(<DeviceSettings />);
      await user.click(screen.getByRole("button", { name: "Use profile settings" }));

      expect(mocks.clearDevice).not.toHaveBeenCalled();
    });

    it("offers nothing to reset on a device with no changes", () => {
      render(<DeviceSettings />);

      expect(
        screen.queryByRole("button", { name: "Use profile settings" }),
      ).not.toBeInTheDocument();
    });
  });
});

describe("DeviceSettings initial device", () => {
  beforeEach(() => {
    mocks.profile = { id: "profile-1", is_primary: false };
    mocks.useEffectiveSettings.mockReset();
    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useStoredSettingValues.mockReset();
    mocks.useStoredSettingValues.mockReturnValue({ data: undefined });
    mocks.capabilities.data = undefined;
    mocks.capabilities.isError = true;
  });

  function lastOpenedDevice() {
    const calls = mocks.useEffectiveSettings.mock.calls;
    const options = calls[calls.length - 1]?.[0] as { deviceId: string; profileId?: string };
    return { deviceId: options.deviceId, profileId: options.profileId };
  }

  it("opens on the device you are using even when another was seen more recently", () => {
    // The server orders by last use, so another browser on the same account
    // can sit above the one you are on.
    mocks.ownDevices = [
      device({
        device_id: "safari",
        device_name: "Safari on macOS",
        device_platform: "web",
        is_current_device: false,
      }),
      device({ device_id: "chrome", device_name: "Chrome on Windows", device_platform: "web" }),
    ];

    render(<DeviceSettings />);

    expect(lastOpenedDevice()).toEqual({ deviceId: "chrome", profileId: undefined });
    expect(screen.getByRole("heading", { name: "Chrome on Windows" })).toBeInTheDocument();
  });

  it("falls back to the first device when the one you are using is not listed", () => {
    mocks.ownDevices = [
      device({ device_id: "safari", device_name: "Safari on macOS", is_current_device: false }),
      device({ device_id: "ipad", device_name: "iPad", is_current_device: false }),
    ];

    render(<DeviceSettings />);

    expect(lastOpenedDevice()).toEqual({ deviceId: "safari", profileId: undefined });
  });

  it("opens on your own row for the current device in the household view", async () => {
    const user = userEvent.setup();
    mocks.profile = { id: "profile-1", is_primary: true };
    mocks.ownDevices = [
      device({ device_id: "safari", device_name: "Safari on macOS", is_current_device: false }),
      device({ device_id: "chrome", device_name: "Chrome on Windows" }),
    ];
    // Another profile on this account used the same browser more recently,
    // so the same device appears twice and both rows are flagged current.
    mocks.householdDevices = [
      device({
        device_id: "chrome",
        device_name: "Chrome on Windows",
        profile_id: "profile-2",
        profile_name: "Alex",
      }),
      device({ device_id: "safari", device_name: "Safari on macOS", is_current_device: false }),
      device({ device_id: "chrome", device_name: "Chrome on Windows" }),
    ];

    render(<DeviceSettings />);
    expect(lastOpenedDevice()).toEqual({ deviceId: "chrome", profileId: undefined });

    await user.click(screen.getByRole("button", { name: /Everyone/ }));

    // Still your own Chrome, not Alex's, even though Alex's row comes first.
    expect(lastOpenedDevice()).toEqual({ deviceId: "chrome", profileId: undefined });
  });
});
