// @vitest-environment jsdom
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminUser } from "@/api/types";
import { SETTING_KEYS } from "@/lib/settingsContract";

import { PreferencesTab } from "./PreferencesTab";

const mocks = vi.hoisted(() => ({
  settings: [] as unknown[],
  devices: [] as unknown[],
  updateMutate: vi.fn(),
  updateMutateAsync: vi.fn(),
  deleteMutate: vi.fn(),
  clearDeviceMutate: vi.fn(),
}));

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUserSettings: () => ({ data: mocks.settings, isLoading: false, isError: false }),
  useAdminUserCapabilities: () => ({ data: { account_devices: true } }),
  useUpdateAdminUserSetting: () => ({
    mutate: mocks.updateMutate,
    mutateAsync: mocks.updateMutateAsync,
    isPending: false,
  }),
  useDeleteAdminUserSetting: () => ({ mutate: mocks.deleteMutate, isPending: false }),
  useDeleteAllAdminUserDeviceSettingsForDevice: () => ({
    mutate: mocks.clearDeviceMutate,
    isPending: false,
  }),
}));
vi.mock("@/hooks/queries/admin/userActivity", () => ({
  useAdminUserDevices: () => ({ data: mocks.devices }),
}));
vi.mock("@/hooks/queries/admin/history", () => ({
  useAdminUserProfiles: () => ({
    data: [
      { id: "p1", name: "Main" },
      { id: "p2", name: "Kids" },
    ],
  }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [{ id: 3, name: "TV Shows" }] }),
}));

const user = { id: 7, username: "taylor" } as AdminUser;
const SUBTITLES = SETTING_KEYS.PLAYBACK_SUBTITLE_MODE;
const RECAPS = SETTING_KEYS.PLAYBACK_AUTO_SKIP_RECAP;

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

function levelCard(title: string) {
  return screen.getByRole("heading", { name: title }).closest("section") as HTMLElement;
}

const shield = {
  device_id: "dev-shield",
  device_name: "Shield TV",
  device_platform: "Android TV",
  last_seen_at: "2026-09-25T10:00:00Z",
  last_updated: null,
  override_count: 1,
  profiles: [
    { profile_id: "p1", profile_name: "Main", override_count: 1, last_seen_at: null },
    { profile_id: "p2", profile_name: "Kids", override_count: 0, last_seen_at: null },
  ],
};

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", MockResizeObserver);
  installPointerCaptureMocks();
  mocks.settings = [
    { key: SUBTITLES, scope: "profile", profile_id: "p1", value: "off" },
    { key: RECAPS, scope: "profile", profile_id: "p1", value: "true" },
    {
      key: RECAPS,
      scope: "profile_device",
      profile_id: "p1",
      device_id: "dev-shield",
      value: "false",
    },
    {
      key: SUBTITLES,
      scope: "profile_library",
      profile_id: "p1",
      library_id: 3,
      value: "always",
    },
  ];
  mocks.devices = [shield];
  for (const mock of Object.values(mocks)) {
    if (typeof mock === "function") mock.mockReset();
  }
  mocks.updateMutateAsync.mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("PreferencesTab levels", () => {
  it("adds a setting at the level's identity, starting from the value it replaces", async () => {
    const u = userEvent.setup();
    renderTab("?tab=preferences&level=device.p1.dev-shield");
    const card = levelCard("Shield TV · Main");
    await u.click(within(card).getByRole("button", { name: /Add a setting for this device/ }));

    // Already stored on this device: listed, not selectable.
    expect(screen.getByRole("checkbox", { name: /Auto-skip recaps/ })).toBeDisabled();
    await u.type(screen.getByRole("searchbox", { name: "Search settings" }), "subtitles");
    const subtitles = screen.getByRole("checkbox", { name: /^Subtitles/ });
    expect(subtitles).toHaveTextContent("Main: Off");
    await u.click(subtitles);
    await u.click(screen.getByRole("button", { name: "Add setting" }));

    await waitFor(() => expect(mocks.updateMutateAsync).toHaveBeenCalledTimes(1));
    expect(mocks.updateMutateAsync).toHaveBeenCalledWith({
      userId: 7,
      key: SUBTITLES,
      identity: { scope: "profile_device", profileId: "p1", deviceId: "dev-shield" },
      value: "off",
    });
  });

  it("confirms removing a device value and deletes exactly that row", async () => {
    const u = userEvent.setup();
    renderTab("?tab=preferences&level=device.p1.dev-shield");
    await u.click(screen.getByRole("button", { name: "Remove Auto-skip recaps" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Use Main's value on Shield TV?");
    expect(dialog).toHaveTextContent(
      "Auto-skip recaps goes back to Main's setting, Enabled, on Shield TV. The change reaches the app the next time it syncs.",
    );
    await u.click(within(dialog).getByRole("button", { name: "Use Main's" }));
    expect(mocks.deleteMutate).toHaveBeenCalledWith({
      userId: 7,
      key: RECAPS,
      identity: {
        scope: "profile_device",
        profileId: "p1",
        clientFamily: undefined,
        deviceId: "dev-shield",
        libraryId: undefined,
        seriesId: undefined,
      },
    });
  });

  it("offers the app default when a profile value is removed", async () => {
    const u = userEvent.setup();
    renderTab("?tab=preferences&level=profile.p1");
    await u.click(screen.getByRole("button", { name: "Remove Subtitles" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Go back to the app default?");
    expect(dialog).toHaveTextContent("Subtitles goes back to the app default, Auto, for Main.");
    await u.click(within(dialog).getByRole("button", { name: "Use app default" }));
    expect(mocks.deleteMutate.mock.calls[0]?.[0]).toMatchObject({
      key: SUBTITLES,
      identity: { scope: "profile", profileId: "p1", deviceId: undefined },
    });
  });

  it("clears one profile's settings on a device", async () => {
    const u = userEvent.setup();
    renderTab("?tab=preferences&level=device.p1.dev-shield");
    const card = levelCard("Shield TV · Main");
    expect(within(card).getByRole("link", { name: /Open device/ })).toHaveAttribute(
      "href",
      "/admin/devices/7/dev-shield",
    );
    await u.click(within(card).getByRole("button", { name: "Clear device settings" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Clear Main's settings on Shield TV?");
    await u.click(within(dialog).getByRole("button", { name: "Clear settings" }));
    expect(mocks.clearDeviceMutate).toHaveBeenCalledWith({
      userId: 7,
      profileId: "p1",
      deviceId: "dev-shield",
      keys: [RECAPS],
    });
  });

  it("edits a structured setting through the JSON editor", async () => {
    const u = userEvent.setup();
    mocks.settings = [
      { key: "ui.sidebar_pins", scope: "profile", profile_id: "p2", value: '["home"]' },
    ];
    renderTab("?tab=preferences&level=profile.p2");
    const card = levelCard("All devices · Kids");
    expect(within(card).queryByRole("combobox")).not.toBeInTheDocument();
    await u.click(within(card).getByRole("button", { name: "Edit JSON" }));
    expect(screen.getByRole("textbox", { name: "Raw value" })).toHaveValue('["home"]');
    await u.click(screen.getByRole("button", { name: "Save value" }));
    expect(mocks.updateMutate.mock.calls[0]?.[0]).toMatchObject({
      key: "ui.sidebar_pins",
      identity: { scope: "profile", profileId: "p2" },
      value: '["home"]',
    });
  });

  it("shows a setting this version doesn't know without an editor", () => {
    mocks.settings = [
      { key: "future.setting", scope: "profile", profile_id: "p2", value: '{"a":1}' },
    ];
    renderTab("?tab=preferences&level=profile.p2");
    const card = levelCard("All devices · Kids");
    expect(within(card).getByText("future.setting")).toBeInTheDocument();
    expect(within(card).getByText("View only")).toBeInTheDocument();
    expect(within(card).queryByRole("button", { name: "Edit JSON" })).not.toBeInTheDocument();
    expect(within(card).getByRole("button", { name: "Remove future.setting" })).toBeEnabled();
  });
});
