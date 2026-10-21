// @vitest-environment jsdom

import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AdminDeviceSetting, AdminUserSettingEntry } from "@/hooks/queries/admin/users";

import { stubRadixDom } from "./adminPage.test-support";

stubRadixDom();

const profiles = [
  {
    profile_id: "alex",
    profile_name: "Alex",
    override_count: 1,
    last_updated: "2026-10-01T00:00:00Z",
  },
];
const device = {
  user_id: 1,
  username: "admin",
  email: "admin@example.test",
  device_id: "tv-1",
  device_name: "Living Room TV",
  device_platform: "tvOS",
  override_count: 1,
  profile_count: 1,
  profiles,
  last_updated: "2026-10-01T00:00:00Z",
};
const hdrOverride: AdminDeviceSetting = {
  user_id: 1,
  profile_id: "alex",
  profile_name: "Alex",
  device_id: "tv-1",
  device_name: "Living Room TV",
  device_platform: "tvOS",
  key: "player.hdr_enabled",
  value: "false",
  updated_at: "2026-10-01T00:00:00Z",
};
// What GET /admin/users/1/settings/values returns for this account: the
// device's own HDR choice, and the profile's audio language.
const stored: AdminUserSettingEntry[] = [
  {
    key: "player.hdr_enabled",
    value: "false",
    scope: "profile_device",
    profile_id: "alex",
    device_id: "tv-1",
  },
  { key: "playback.audio_language", value: "fr", scope: "profile", profile_id: "alex" },
];

const settingsQuery = vi.hoisted(() => ({ isError: false }));

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminDevices: () => ({ data: [device], isLoading: false }),
  useAdminDeviceDetail: () => ({ data: { ...device, settings: [] }, isLoading: false }),
  useAdminDeviceOverrides: () =>
    settingsQuery.isError
      ? { data: [], isLoading: false, isError: true }
      : { data: [hdrOverride], isLoading: false, isError: false },
  useAdminUserSettings: () =>
    settingsQuery.isError
      ? { data: [], isLoading: false, isError: true }
      : { data: stored, isLoading: false, isError: false },
  useUpdateAdminUserDeviceSetting: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteAdminUserDeviceSetting: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteAllAdminUserDeviceSettingsForDevice: () => ({ mutate: vi.fn(), isPending: false }),
}));

import AdminDevices from "./AdminDevices";

function renderDetail() {
  render(
    <MemoryRouter initialEntries={["/admin/devices/1/tv-1"]}>
      <Routes>
        <Route path="/admin/devices/:userId/:deviceId" element={<AdminDevices />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("AdminDevices device detail", () => {
  afterEach(() => {
    settingsQuery.isError = false;
  });

  it("reports a failed settings read instead of showing every row as the app default", () => {
    settingsQuery.isError = true;
    renderDetail();

    expect(screen.getByText("Couldn't load this device's settings")).toBeInTheDocument();
    expect(screen.queryByText("playback.audio_language")).not.toBeInTheDocument();
  });
});
