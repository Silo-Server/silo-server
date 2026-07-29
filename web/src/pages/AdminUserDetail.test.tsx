// @vitest-environment jsdom

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminUser, UpdateUserRequest } from "@/api/types";
import { SETTING_KEYS } from "@/lib/settingsContract";

import AdminUserDetail from "./AdminUserDetail";

interface UpdateUserMutationArg {
  id: number;
  body: UpdateUserRequest;
}

const mocks = vi.hoisted(() => ({
  updateUserMutate: vi.fn(),
  beginImpersonation: vi.fn(),
  updateSettingMutate: vi.fn(),
  /** Rows the canonical admin settings list answers with, per test. */
  userSettings: [] as unknown[],
}));

const adminUser: AdminUser = {
  id: 7,
  username: "taylor",
  email: "taylor@example.test",
  role: "user",
  permissions: [],
  enabled: true,
  library_ids: null,
  access_group_id: null,
  max_playback_quality: "source",
  max_streams: 0,
  max_transcodes: 0,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_profiles: 4,
  download_allowed: true,
  download_transcode_allowed: true,
  created_at: "2026-07-01T12:00:00Z",
  updated_at: "2026-07-01T12:00:00Z",
};

class MockResizeObserver implements ResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

function installPointerCaptureMocks() {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: {
      configurable: true,
      value: () => false,
    },
    setPointerCapture: {
      configurable: true,
      value: () => {},
    },
    releasePointerCapture: {
      configurable: true,
      value: () => {},
    },
    scrollIntoView: {
      configurable: true,
      value: () => {},
    },
  });
}

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUser: () => ({ data: adminUser, isLoading: false, error: null }),
  useUpdateUser: () => ({ mutate: mocks.updateUserMutate, isPending: false }),
  useDeleteUser: () => ({ mutate: vi.fn(), isPending: false }),
  useImpersonateUser: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useAdminUserDeviceSettings: () => ({ data: [], isLoading: false }),
  useAdminUserSettings: () => ({ data: mocks.userSettings, isLoading: false }),
  useDeleteAdminUserDeviceSetting: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteAdminUserSetting: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteAllAdminUserDeviceSettingsForDevice: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateAdminUserDeviceSetting: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateAdminUserSetting: () => ({ mutate: mocks.updateSettingMutate, isPending: false }),
}));

vi.mock("@/hooks/queries/admin/accessGroups", () => ({
  useAccessGroups: () => ({
    data: [
      {
        id: 3,
        name: "Kids",
        description: "",
        library_ids: null,
        max_playback_quality: "source",
        download_allowed: true,
        download_transcode_allowed: true,
        max_streams: 0,
        max_transcodes: 0,
        allowed_permissions: null,
        requests_allowed: true,
        member_count: 0,
        created_at: "2026-07-01T12:00:00Z",
        updated_at: "2026-07-01T12:00:00Z",
      },
      {
        id: 5,
        name: "Guests",
        description: "",
        library_ids: [],
        max_playback_quality: "720p",
        download_allowed: false,
        download_transcode_allowed: false,
        max_streams: 1,
        max_transcodes: 0,
        allowed_permissions: [],
        requests_allowed: false,
        member_count: 0,
        created_at: "2026-07-01T12:00:00Z",
        updated_at: "2026-07-01T12:00:00Z",
      },
    ],
  }),
}));

vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: [] }),
}));

vi.mock("@/hooks/queries/admin/history", () => ({
  useAdminUserProfiles: () => ({ data: [], isLoading: false }),
  useAdminPlaybackHistory: () => ({ data: { entries: [] }, isLoading: false }),
}));

vi.mock("@/hooks/queries/admin/ips", () => ({
  useUserIPs: () => ({ data: [], isLoading: false }),
}));

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ beginImpersonation: mocks.beginImpersonation }),
}));

function renderUserDetail() {
  render(
    <MemoryRouter initialEntries={["/admin/users/7"]}>
      <Routes>
        <Route path="/admin/users/:id" element={<AdminUserDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.stubGlobal("ResizeObserver", MockResizeObserver);
  installPointerCaptureMocks();
  mocks.updateUserMutate.mockReset();
  mocks.beginImpersonation.mockReset();
  mocks.updateSettingMutate.mockReset();
  mocks.userSettings = [];
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AdminUserDetail access group picker", () => {
  it("renders group options and includes access_group_id in the save payload", async () => {
    const user = userEvent.setup();
    renderUserDetail();

    expect(screen.getByText("Group")).toBeInTheDocument();
    expect(screen.getByText("None")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /edit/i }));
    await user.click(screen.getByRole("tab", { name: "Access" }));

    const groupSelect = screen.getByRole("combobox", { name: "Group" });
    await user.click(groupSelect);
    await user.click(await screen.findByRole("option", { name: "Guests" }));

    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalled());
    const call = mocks.updateUserMutate.mock.calls[0]?.[0] as UpdateUserMutationArg | undefined;
    expect(call).toBeDefined();
    expect(call?.id).toBe(7);
    expect(call?.body.access_group_id).toBe(5);
  });
});

describe("AdminUserDetail user settings tab", () => {
  const pins = JSON.stringify({ "1": [{ type: "collection", id: "42", label: "Pinned Horror" }] });

  it("edits an object-valued setting through the JSON editor, not a select", async () => {
    // Every non-device canonical row lands in this tab, including the
    // object-valued profile settings. controlKindFor has no `object` branch, so
    // an unguarded definition falls through to RegistrySettingControl's select —
    // which for a nullable object with no enum members renders a single "Unset"
    // item whose only effect is to null the value and destroy the user's pins.
    const user = userEvent.setup();
    mocks.userSettings = [
      {
        key: SETTING_KEYS.UI_SIDEBAR_PINS,
        scope: "profile",
        profile_id: "profile-1",
        value: pins,
      },
    ];
    renderUserDetail();

    await user.click(screen.getByRole("tab", { name: "Settings" }));

    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit JSON" }));

    const editor = screen.getByRole("textbox", { name: "Raw value" });
    expect(editor).toHaveValue(pins);

    const edited = JSON.stringify({ "1": [{ type: "collection", id: "43" }] });
    await user.clear(editor);
    await user.type(editor, edited.replace(/[{[]/g, "$&$&"));
    await user.click(screen.getByRole("button", { name: "Save value" }));

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
    const user = userEvent.setup();
    mocks.userSettings = [
      {
        key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
        scope: "profile",
        profile_id: "profile-1",
        value: "false",
      },
    ];
    renderUserDetail();

    await user.click(screen.getByRole("tab", { name: "Settings" }));

    expect(screen.queryByRole("button", { name: "Edit JSON" })).not.toBeInTheDocument();
    const toggle = screen.getByRole("switch");
    expect(toggle).not.toBeChecked();
    await user.click(toggle);

    await waitFor(() => expect(mocks.updateSettingMutate).toHaveBeenCalled());
    expect(mocks.updateSettingMutate.mock.calls[0]?.[0]).toMatchObject({
      key: SETTING_KEYS.PLAYBACK_AUTO_SKIP_INTRO,
      value: "true",
    });
  });
});

describe("AdminUserDetail transcode limits", () => {
  it("disables transcoding and includes the flag in the save payload", async () => {
    const user = userEvent.setup();
    renderUserDetail();

    await user.click(screen.getByRole("button", { name: /edit/i }));
    await user.click(screen.getByRole("tab", { name: "Limits" }));
    expect(screen.queryByRole("switch", { name: "Audio transcodes" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Disable video transcoding" }));

    expect(screen.getByText("Video transcoding disabled")).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: "Max Transcodes" })).toBeDisabled();
    const audioTranscodeSwitch = screen.getByRole("switch", { name: "Audio transcodes" });
    expect(audioTranscodeSwitch).toBeChecked();
    await user.click(audioTranscodeSwitch);

    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalled());
    const call = mocks.updateUserMutate.mock.calls[0]?.[0] as UpdateUserMutationArg | undefined;
    expect(call?.body.transcode_allowed).toBe(false);
    expect(call?.body.audio_transcode_allowed).toBe(false);
  });
});
