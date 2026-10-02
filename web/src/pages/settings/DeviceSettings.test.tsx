// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { SettingsCapabilities } from "@/hooks/queries/settingValues";

const mocks = vi.hoisted(() => ({
  refetchCapabilities: vi.fn(),
  useEffectiveSettings: vi.fn(),
  useStoredSettingValues: vi.fn(),
  deviceSettingGroupsProps: vi.fn(),
  capabilities: {
    data: undefined as SettingsCapabilities | undefined,
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
    mocks.refetchCapabilities.mockReset();
    mocks.useEffectiveSettings.mockReset();
    mocks.useEffectiveSettings.mockReturnValue({ data: {}, isLoading: false });
    mocks.useStoredSettingValues.mockReset();
    mocks.useStoredSettingValues.mockReturnValue({ data: undefined });
    mocks.deviceSettingGroupsProps.mockReset();
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
});
