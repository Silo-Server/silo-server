// @vitest-environment jsdom
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminUser } from "@/api/types";
import { SETTING_KEYS } from "@/lib/settingsContract";

import { PreferencesTab } from "./PreferencesTab";

const mocks = vi.hoisted(() => ({
  userSettings: [] as unknown[],
  deviceSettings: [] as unknown[],
  updateSettingMutate: vi.fn(),
  deleteSettingMutate: vi.fn(),
  updateDeviceMutate: vi.fn(),
  deleteDeviceSettingMutate: vi.fn(),
  deleteAllDeviceMutate: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUserSettings: () => ({ data: mocks.userSettings, isLoading: false, isError: false }),
  useAdminUserDeviceSettings: () => ({
    data: mocks.deviceSettings,
    isLoading: false,
    isError: false,
  }),
  useAdminUserSettingCounts: () => ({
    account: mocks.userSettings.length,
    device: mocks.deviceSettings.length,
    isLoading: false,
    isError: false,
  }),
  useUpdateAdminUserSetting: () => ({ mutate: mocks.updateSettingMutate, isPending: false }),
  useDeleteAdminUserSetting: () => ({ mutate: mocks.deleteSettingMutate, isPending: false }),
  useUpdateAdminUserDeviceSetting: () => ({ mutate: mocks.updateDeviceMutate, isPending: false }),
  useDeleteAdminUserDeviceSetting: () => ({
    mutate: mocks.deleteDeviceSettingMutate,
    isPending: false,
  }),
  useDeleteAllAdminUserDeviceSettingsForDevice: () => ({
    mutate: mocks.deleteAllDeviceMutate,
    isPending: false,
  }),
}));
vi.mock("@/hooks/queries/admin/history", () => ({
  useAdminUserProfiles: () => ({
    data: [
      { id: "profile-1", name: "Main" },
      { id: "profile-2", name: "Kids" },
    ],
  }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [] }),
}));

const user = { id: 7, username: "taylor" } as AdminUser;

class MockResizeObserver implements ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function installPointerCaptureMocks() {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="search">{location.search}</output>;
}

function renderTab(search = "?tab=preferences") {
  render(
    <MemoryRouter initialEntries={[`/admin/users/7${search}`]}>
      <Routes>
        <Route
          path="/admin/users/:id"
          element={
            <>
              <PreferencesTab user={user} />
              <LocationProbe />
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  );
}

function card(title: string) {
  const heading = screen.getByRole("heading", { name: title });
  return heading.closest("section") as HTMLElement;
}

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", MockResizeObserver);
  installPointerCaptureMocks();
  mocks.userSettings = [];
  mocks.deviceSettings = [];
  for (const mock of [
    mocks.updateSettingMutate,
    mocks.deleteSettingMutate,
    mocks.updateDeviceMutate,
    mocks.deleteDeviceSettingMutate,
    mocks.deleteAllDeviceMutate,
  ])
    mock.mockReset();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("PreferencesTab account settings", () => {
  const pins = JSON.stringify({ "1": [{ type: "collection", id: "42", label: "Pinned Horror" }] });

  it("edits an object-valued setting through the JSON editor, not a select", async () => {
    // Object-valued profile settings have no inline widget. An unguarded
    // definition would fall through to RegistrySettingControl's select, which
    // for a nullable object with no enum members renders a single "Unset" item
    // whose only effect is to null the value and destroy the user's pins.
    const u = userEvent.setup();
    mocks.userSettings = [
      { key: SETTING_KEYS.UI_SIDEBAR_PINS, scope: "profile", profile_id: "profile-1", value: pins },
    ];
    renderTab();

    const main = card("Profile · Main");
    expect(within(main).queryByRole("combobox")).not.toBeInTheDocument();
    await u.click(within(main).getByRole("button", { name: "Edit JSON" }));

    const editor = screen.getByRole("textbox", { name: "Raw value" });
    expect(editor).toHaveValue(pins);

    const edited = JSON.stringify({ "1": [{ type: "collection", id: "43" }] });
    await u.clear(editor);
    await u.type(editor, edited.replace(/[{[]/g, "$&$&"));
    await u.click(screen.getByRole("button", { name: "Save value" }));

    await waitFor(() => expect(mocks.updateSettingMutate).toHaveBeenCalled());
    const call = mocks.updateSettingMutate.mock.calls[0]?.[0] as {
      key: string;
      value: string;
      identity: { scope: string; profileId?: string };
    };
    expect(call.key).toBe(SETTING_KEYS.UI_SIDEBAR_PINS);
    expect(call.identity).toMatchObject({ scope: "profile", profileId: "profile-1" });
    expect(JSON.parse(call.value)).toEqual(JSON.parse(edited));
  });

  it("still renders an inline control for a scalar setting", async () => {
    const u = userEvent.setup();
    mocks.userSettings = [
      {
        key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
        scope: "profile",
        profile_id: "profile-1",
        value: "false",
      },
    ];
    renderTab();

    expect(screen.queryByRole("button", { name: "Edit JSON" })).not.toBeInTheDocument();
    const toggle = screen.getByRole("switch");
    expect(toggle).not.toBeChecked();
    await u.click(toggle);

    await waitFor(() => expect(mocks.updateSettingMutate).toHaveBeenCalled());
    expect(mocks.updateSettingMutate.mock.calls[0]?.[0]).toMatchObject({
      key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
      value: "true",
    });
  });

  it("keeps client family in profile-client grouping and mutation identity", async () => {
    const u = userEvent.setup();
    const value = JSON.stringify({ poster_size: "compact", caption: "title" });
    mocks.userSettings = [
      {
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        scope: "profile_client",
        profile_id: "profile-1",
        client_family: "tv",
        value,
      },
      {
        key: SETTING_KEYS.UI_CARD_PRESENTATION,
        scope: "profile_client",
        profile_id: "profile-1",
        client_family: "web",
        value,
      },
    ];
    renderTab();

    const tv = card("Profile · Main · TV apps");
    expect(card("Profile · Main · Web apps")).toBeInTheDocument();
    await u.click(within(tv).getByRole("button", { name: "Reset" }));
    expect(mocks.deleteSettingMutate).toHaveBeenCalledWith({
      userId: 7,
      key: SETTING_KEYS.UI_CARD_PRESENTATION,
      identity: {
        scope: "profile_client",
        profileId: "profile-1",
        clientFamily: "tv",
        libraryId: undefined,
        seriesId: undefined,
      },
    });

    await u.click(within(tv).getByRole("button", { name: "Edit JSON" }));
    await u.click(screen.getByRole("button", { name: "Save value" }));

    await waitFor(() => expect(mocks.updateSettingMutate).toHaveBeenCalled());
    expect(mocks.updateSettingMutate.mock.calls[0]?.[0]).toMatchObject({
      key: SETTING_KEYS.UI_CARD_PRESENTATION,
      identity: { scope: "profile_client", profileId: "profile-1", clientFamily: "tv" },
    });
  });

  it("groups by scope and narrows with search and the scope filter", async () => {
    const u = userEvent.setup();
    mocks.userSettings = [
      { key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO, scope: "account", value: "true" },
      {
        key: SETTING_KEYS.UI_SIDEBAR_PINS,
        scope: "profile",
        profile_id: "profile-2",
        value: "{}",
      },
    ];
    renderTab();

    expect(screen.getByRole("tab", { name: "Account & profiles · 2" })).toBeInTheDocument();
    expect(card("Account-wide")).toHaveTextContent("Applies to every profile");
    expect(card("Profile · Kids")).toBeInTheDocument();

    await u.type(screen.getByRole("searchbox", { name: "Search settings" }), "sidebar_pins");
    expect(screen.queryByRole("heading", { name: "Account-wide" })).not.toBeInTheDocument();
    expect(card("Profile · Kids")).toBeInTheDocument();

    await u.click(screen.getByRole("combobox", { name: "Scope" }));
    await u.click(await screen.findByRole("option", { name: "Account-wide" }));
    expect(screen.getByText("No settings match.")).toBeInTheDocument();

    await u.clear(screen.getByRole("searchbox", { name: "Search settings" }));
    expect(card("Account-wide")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Profile · Kids" })).not.toBeInTheDocument();
  });

  it("switches to the per-device view through the URL", async () => {
    const u = userEvent.setup();
    renderTab();
    await u.click(screen.getByRole("tab", { name: /Per device/ }));
    expect(screen.getByTestId("search")).toHaveTextContent("?tab=preferences&view=devices");
    expect(screen.getByText("No device overrides")).toBeInTheDocument();
  });
});

describe("PreferencesTab per device", () => {
  function deviceSetting(profileId: string, profileName: string, key: string, value = "true") {
    return {
      user_id: 7,
      profile_id: profileId,
      profile_name: profileName,
      device_id: "a91f03c2-device",
      device_name: "Apple TV 4K",
      device_platform: "tvOS",
      key,
      value,
      updated_at: "2026-09-27T10:00:00Z",
    };
  }

  it("names the profile and device a reset clears", async () => {
    const u = userEvent.setup();
    mocks.deviceSettings = [
      deviceSetting("profile-1", "Main", SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO),
      deviceSetting("profile-2", "Kids", SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO),
      deviceSetting("profile-2", "Kids", SETTING_KEYS.UI_SIDEBAR_PINS, "{}"),
    ];
    renderTab("?tab=preferences&view=devices");

    expect(screen.getByText("1 device · 3 saved settings · 2 profiles")).toBeInTheDocument();
    const device = card("Apple TV 4K");
    await u.click(within(device).getByRole("tab", { name: "Kids" }));
    await u.click(within(device).getByRole("button", { name: "Reset Kids on this device" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Reset Kids on Apple TV 4K?");
    expect(dialog).toHaveTextContent(
      "Every setting Kids saved on this device is cleared. Playback falls back to account or default values.",
    );
    await u.click(within(dialog).getByRole("button", { name: "Reset" }));

    expect(mocks.deleteAllDeviceMutate).toHaveBeenCalledWith({
      userId: 7,
      profileId: "profile-2",
      deviceId: "a91f03c2-device",
      keys: [SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO, SETTING_KEYS.UI_SIDEBAR_PINS],
    });
  });

  it("confirms before resetting one override", async () => {
    const u = userEvent.setup();
    mocks.deviceSettings = [
      deviceSetting("profile-1", "Main", SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO),
    ];
    renderTab("?tab=preferences&view=devices");

    const device = card("Apple TV 4K");
    await u.click(within(device).getByRole("button", { name: "Reset" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Reset this override?");
    await u.click(within(dialog).getByRole("button", { name: "Reset override" }));
    expect(mocks.deleteDeviceSettingMutate).toHaveBeenCalledWith({
      userId: 7,
      profileId: "profile-1",
      deviceId: "a91f03c2-device",
      key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
    });
  });
});
