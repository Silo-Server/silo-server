// @vitest-environment jsdom

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminUser, UpdateUserRequest } from "@/api/types";

import AdminUserDetail from "./AdminUserDetail";

interface UpdateUserMutationArg {
  id: number;
  body: UpdateUserRequest;
}

const mocks = vi.hoisted(() => ({
  updateUserMutate: vi.fn(),
  beginImpersonation: vi.fn(),
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
  useAdminUserSettings: () => ({ data: [], isLoading: false }),
  useDeleteAdminUserDeviceSetting: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteAdminUserSetting: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteAllAdminUserDeviceSettingsForDevice: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateAdminUserDeviceSetting: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateAdminUserSetting: () => ({ mutate: vi.fn(), isPending: false }),
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

describe("AdminUserDetail access group picker", () => {
  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", MockResizeObserver);
    installPointerCaptureMocks();
    mocks.updateUserMutate.mockReset();
    mocks.beginImpersonation.mockReset();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

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
