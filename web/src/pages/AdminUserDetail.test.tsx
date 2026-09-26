import { V2ProblemError } from "@/api/v2/request";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
// @vitest-environment jsdom

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminUser, UpdateUserRequest } from "@/api/types";
import { PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION } from "@/lib/permissions";
import { SETTING_KEYS } from "@/lib/settingsContract";

import AdminUserDetail from "./AdminUserDetail";

interface UpdateUserMutationArg {
  editor: { user: { id: number } };
  body: UpdateUserRequest;
}

const mocks = vi.hoisted(() => ({
  /** The signed-in account and whether it is the server owner. */
  viewer: { id: 1 } as { id: number } | null,
  viewerIsOwner: false,
  updateUserMutate: vi.fn(),
  getReads: 0,
  impersonate: vi.fn(),
  beginImpersonation: vi.fn(),
  updateSettingMutate: vi.fn(),
  deleteSettingMutate: vi.fn(),
  /** Rows the canonical admin settings list answers with, per test. */
  userSettings: [] as unknown[],
  /** The account the detail page renders, reset to `adminUser` per test. */
  user: null as AdminUser | null,
  userError: null as Error | null,
  refetchUser: vi.fn(),
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
  max_playback_quality: null,
  max_streams: null,
  max_transcodes: null,
  max_remote_stream_bitrate_kbps: null,
  max_local_stream_bitrate_kbps: null,
  transcode_allowed: null,
  audio_transcode_allowed: null,
  max_profiles: 4,
  download_allowed: null,
  download_transcode_allowed: null,
  requests_allowed: null,
  password_login: true,
  password_change_required: false,
  is_owner: false,
  effective_policy: {
    library_ids: null,
    max_playback_quality: "",
    max_streams: 0,
    max_transcodes: 0,
    max_remote_stream_bitrate_kbps: 0,
    max_local_stream_bitrate_kbps: 0,
    transcode_allowed: true,
    audio_transcode_allowed: true,
    download_allowed: true,
    download_transcode_allowed: true,
    requests_allowed: true,
    permissions: [],
  },
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

vi.mock("@/api/v2/adminUsers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/adminUsers")>()),
  getAdminUser: async () => ({
    user: mocks.user!,
    etag: `"read-${++mocks.getReads}"`,
    profileContext: (await import("@/api/client")).captureProfileRequestContext()!,
  }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({
  useViewerIsOwner: () => mocks.viewerIsOwner,
  useAdminUserCapabilities: () => ({ data: { available: true, default_profile: true } }),
  useAdminUser: () => ({
    data: mocks.user ?? undefined,
    isLoading: false,
    isFetching: false,
    error: mocks.userError,
    refetch: mocks.refetchUser,
  }),
  useUpdateUser: () => ({ mutateAsync: mocks.updateUserMutate, isPending: false }),
  useDeleteUser: () => ({ mutate: vi.fn(), isPending: false }),
  useImpersonateUser: () => ({ mutateAsync: mocks.impersonate, reset: vi.fn(), isPending: false }),
  useIssuePasswordReset: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useAdminUserDeviceSettings: () => ({ data: [], isLoading: false }),
  useAdminUserSettings: () => ({ data: mocks.userSettings, isLoading: false }),
  useDeleteAdminUserDeviceSetting: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteAdminUserSetting: () => ({ mutate: mocks.deleteSettingMutate, isPending: false }),
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
        transcode_allowed: true,
        audio_transcode_allowed: true,
        max_streams: 0,
        max_transcodes: 0,
        max_remote_stream_bitrate_kbps: 0,
        max_local_stream_bitrate_kbps: 0,
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
        transcode_allowed: false,
        audio_transcode_allowed: true,
        max_streams: 1,
        max_transcodes: 0,
        max_remote_stream_bitrate_kbps: 8000,
        max_local_stream_bitrate_kbps: 0,
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
  useAuth: () => ({ beginImpersonation: mocks.beginImpersonation, user: mocks.viewer }),
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
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  vi.stubGlobal("ResizeObserver", MockResizeObserver);
  installPointerCaptureMocks();
  mocks.updateUserMutate.mockReset();
  mocks.getReads = 0;
  mocks.impersonate.mockReset();
  mocks.beginImpersonation.mockReset();
  mocks.updateSettingMutate.mockReset();
  mocks.deleteSettingMutate.mockReset();
  mocks.userSettings = [];
  mocks.user = adminUser;
  mocks.viewer = { id: 1 };
  mocks.viewerIsOwner = false;
  mocks.userError = null;
  mocks.refetchUser.mockReset();
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
    expect(call?.editor.user.id).toBe(7);
    expect(call?.body.access_group_id).toBe(5);
  });

  it("clears the group when the account is promoted to admin", async () => {
    const user = userEvent.setup();
    mocks.user = { ...adminUser, access_group_id: 5 };
    renderUserDetail();

    await user.click(screen.getByRole("button", { name: /edit/i }));
    await user.click(screen.getByRole("combobox", { name: "Role" }));
    await user.click(await screen.findByRole("option", { name: "Admin" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalled());
    const call = mocks.updateUserMutate.mock.calls[0]?.[0] as UpdateUserMutationArg | undefined;
    expect(call?.body.role).toBe("admin");
    expect(call?.body.access_group_id).toBeNull();
  });

  it("keeps the picked group when the role is toggled to admin and back", async () => {
    const user = userEvent.setup();
    renderUserDetail();

    await user.click(screen.getByRole("button", { name: /edit/i }));
    await user.click(screen.getByRole("tab", { name: "Access" }));
    await user.click(screen.getByRole("combobox", { name: "Group" }));
    await user.click(await screen.findByRole("option", { name: "Guests" }));

    await user.click(screen.getByRole("tab", { name: "Account" }));
    await user.click(screen.getByRole("combobox", { name: "Role" }));
    await user.click(await screen.findByRole("option", { name: "Admin" }));
    await user.click(screen.getByRole("combobox", { name: "Role" }));
    await user.click(await screen.findByRole("option", { name: "User" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalled());
    const call = mocks.updateUserMutate.mock.calls[0]?.[0] as UpdateUserMutationArg | undefined;
    expect(call?.body.role).toBe("user");
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

  it("keeps client family in profile-client row display and mutation identity", async () => {
    const user = userEvent.setup();
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
    renderUserDetail();

    await user.click(screen.getByRole("tab", { name: "Settings" }));

    const tvIdentity = screen.getByText(/profile profile-1 · family tv$/);
    expect(screen.getByText(/profile profile-1 · family web$/)).toBeInTheDocument();
    const tvRow = tvIdentity.parentElement?.parentElement;
    expect(tvRow).not.toBeNull();
    await user.click(within(tvRow as HTMLElement).getByRole("button", { name: "Reset" }));
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

    await user.click(within(tvRow as HTMLElement).getByRole("button", { name: "Edit JSON" }));
    await user.click(screen.getByRole("button", { name: "Save value" }));

    await waitFor(() => expect(mocks.updateSettingMutate).toHaveBeenCalled());
    expect(mocks.updateSettingMutate.mock.calls[0]?.[0]).toMatchObject({
      key: SETTING_KEYS.UI_CARD_PRESENTATION,
      identity: {
        scope: "profile_client",
        profileId: "profile-1",
        clientFamily: "tv",
      },
    });
  });
});

describe("AdminUserDetail transcode limits", () => {
  it("overrides transcoding gates and includes them in the save payload", async () => {
    const user = userEvent.setup();
    renderUserDetail();

    await user.click(screen.getByRole("button", { name: /edit/i }));
    await user.click(screen.getByRole("tab", { name: "Limits" }));

    // Fields left on their default show the value they resolve to.
    expect(screen.getAllByText("Server default: Unlimited").length).toBeGreaterThan(0);

    await user.click(screen.getByRole("combobox", { name: "Video Transcoding" }));
    await user.click(screen.getByRole("option", { name: "Not allowed" }));
    await user.click(screen.getByRole("combobox", { name: "Audio Transcoding" }));
    await user.click(screen.getByRole("option", { name: "Not allowed" }));

    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalled());
    const call = mocks.updateUserMutate.mock.calls[0]?.[0] as UpdateUserMutationArg | undefined;
    expect(call?.body.transcode_allowed).toBe(false);
    expect(call?.body.audio_transcode_allowed).toBe(false);
    // Untouched policy fields stay inherited (explicit null, not a pinned value).
    expect(call?.body.max_streams).toBeNull();
    expect(call?.body.max_transcodes).toBeNull();
    expect(call?.body.download_allowed).toBeNull();
    expect(call?.body.library_ids).toBeNull();
  });
});

async function openLimitsTab(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /edit/i }));
  await user.click(screen.getByRole("tab", { name: "Limits" }));
}

/** The Override toggle of the nth limit field on the Limits tab. */
function overrideSwitch(index: number): HTMLElement {
  const switches = screen.getAllByRole("switch", { name: "Override" });
  const target = switches[index];
  if (target === undefined) throw new Error(`no Override switch at index ${index}`);
  return target;
}

async function selectGuestsGroup(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("tab", { name: "Access" }));
  await user.click(screen.getByRole("combobox", { name: "Group" }));
  await user.click(await screen.findByRole("option", { name: "Guests" }));
}

describe("AdminUserDetail inherit hints", () => {
  it("derives hints from the group selected in the dialog, on both tabs", async () => {
    const user = userEvent.setup();
    renderUserDetail();

    await openLimitsTab(user);
    // Ungrouped: the server's no-group defaults leave all four ceilings
    // uncapped, and nothing claims to come from a group.
    expect(screen.getAllByText("Server default: Unlimited")).toHaveLength(4);
    expect(screen.queryByText(/Inherited/)).not.toBeInTheDocument();

    await selectGuestsGroup(user);
    // The access tab's hints follow the picker straight away.
    await user.click(screen.getByRole("combobox", { name: "Downloads" }));
    expect(await screen.findByRole("option", { name: "Inherited: Not allowed" })).toBeVisible();
    await user.keyboard("{Escape}");

    // ...and so do the limits tab's, which used to keep reading the stale
    // effective_policy resolved against the account's saved group.
    await user.click(screen.getByRole("tab", { name: "Limits" }));
    expect(screen.getByText("Inherited: 1")).toBeInTheDocument();
    expect(screen.getByText("Inherited: 8 Mbps")).toBeInTheDocument();
    expect(screen.getAllByText("Inherited: Unlimited")).toHaveLength(2);
  });

  it("seeds a limit override from the inherited value, not from unlimited", async () => {
    const user = userEvent.setup();
    renderUserDetail();

    await openLimitsTab(user);
    await selectGuestsGroup(user);
    await user.click(screen.getByRole("tab", { name: "Limits" }));

    await user.click(overrideSwitch(0));
    const maxStreams = screen.getByLabelText("Max Streams");
    expect(maxStreams).toHaveValue(1);

    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalled());
    const call = mocks.updateUserMutate.mock.calls[0]?.[0] as UpdateUserMutationArg | undefined;
    expect(call?.body.max_streams).toBe(1);
  });

  it("treats a cleared limit box as unsaved rather than as explicit unlimited", async () => {
    const user = userEvent.setup();
    renderUserDetail();

    await openLimitsTab(user);
    await user.click(overrideSwitch(0));
    const maxStreams = screen.getByLabelText("Max Streams");

    await user.clear(maxStreams);
    expect(maxStreams).toHaveValue(null);
    expect(
      screen.getByText("Enter a whole number, or turn Override off to use the server default."),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.updateUserMutate).not.toHaveBeenCalled();

    await user.type(maxStreams, "3");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalled());
    const call = mocks.updateUserMutate.mock.calls[0]?.[0] as UpdateUserMutationArg | undefined;
    expect(call?.body.max_streams).toBe(3);
  });

  it("labels an admin's defaults as admin defaults, never as inherited", async () => {
    const user = userEvent.setup();
    mocks.user = { ...adminUser, role: "admin" };
    renderUserDetail();

    await user.click(screen.getByRole("button", { name: /edit/i }));
    await user.click(screen.getByRole("tab", { name: "Access" }));
    expect(screen.getByText("Admin default: All libraries")).toBeInTheDocument();
    await user.click(screen.getByRole("combobox", { name: "Download Transcodes" }));
    expect(await screen.findByRole("option", { name: "Admin default: Not allowed" })).toBeVisible();
    await user.keyboard("{Escape}");

    await user.click(screen.getByRole("tab", { name: "Limits" }));
    expect(screen.getAllByText("Admin default: Unlimited")).toHaveLength(4);
    expect(screen.getByText("Uses the admin default quality ceiling.")).toBeInTheDocument();
    expect(screen.queryByText(/Inherit/)).not.toBeInTheDocument();
  });

  it("follows the role and group pickers from admin default to inherited", async () => {
    const user = userEvent.setup();
    mocks.user = { ...adminUser, role: "admin" };
    renderUserDetail();

    await openLimitsTab(user);
    expect(screen.getAllByText("Admin default: Unlimited")).toHaveLength(4);

    await user.click(screen.getByRole("tab", { name: "Account" }));
    await user.click(screen.getByRole("combobox", { name: "Role" }));
    await user.click(await screen.findByRole("option", { name: "User" }));
    await user.click(screen.getByRole("tab", { name: "Limits" }));
    expect(screen.getAllByText("Server default: Unlimited")).toHaveLength(4);

    await selectGuestsGroup(user);
    await user.click(screen.getByRole("tab", { name: "Limits" }));
    expect(screen.getByText("Inherited: 1")).toBeInTheDocument();
    expect(screen.queryByText(/default/)).not.toBeInTheDocument();
  });
});

describe("AdminUserDetail stream bitrate limits", () => {
  it("overrides a bitrate limit in Mbps, seeded from the inherited cap", async () => {
    const user = userEvent.setup();
    renderUserDetail();

    await openLimitsTab(user);
    await selectGuestsGroup(user);
    await user.click(screen.getByRole("tab", { name: "Limits" }));

    await user.click(overrideSwitch(2));
    const remote = screen.getByRole("combobox", { name: "Max remote stream bitrate" });
    expect(remote).toHaveTextContent("8 Mbps");

    await user.click(remote);
    await user.click(await screen.findByRole("option", { name: "Custom" }));
    const custom = screen.getByLabelText("Max remote stream bitrate in Mbps");
    expect(custom).toHaveValue("8");
    await user.clear(custom);
    await user.type(custom, "2.5");

    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalled());
    const call = mocks.updateUserMutate.mock.calls[0]?.[0] as UpdateUserMutationArg | undefined;
    expect(call?.body.max_remote_stream_bitrate_kbps).toBe(2500);
    expect(call?.body.max_local_stream_bitrate_kbps).toBeNull();
  });

  it("does not save a custom bitrate box until it holds a cap above 0", async () => {
    const user = userEvent.setup();
    renderUserDetail();

    await openLimitsTab(user);
    await user.click(overrideSwitch(3));
    await user.click(screen.getByRole("combobox", { name: "Max local stream bitrate" }));
    await user.click(await screen.findByRole("option", { name: "Custom" }));
    const custom = screen.getByLabelText("Max local stream bitrate in Mbps");
    expect(custom).toHaveValue("");

    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.updateUserMutate).not.toHaveBeenCalled();

    // "0" would be unlimited; the custom box only takes a real cap.
    await user.type(custom, "0");
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.updateUserMutate).not.toHaveBeenCalled();

    await user.clear(custom);
    await user.type(custom, "0.75");
    expect(screen.getByText(/Below 1 Mbps/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalled());
    const call = mocks.updateUserMutate.mock.calls[0]?.[0] as UpdateUserMutationArg | undefined;
    expect(call?.body.max_local_stream_bitrate_kbps).toBe(750);
  });

  it("shows the effective caps in Mbps", () => {
    mocks.user = {
      ...adminUser,
      max_local_stream_bitrate_kbps: 1500,
      effective_policy: {
        ...adminUser.effective_policy,
        max_remote_stream_bitrate_kbps: 8000,
        max_local_stream_bitrate_kbps: 1500,
      },
    };
    renderUserDetail();

    expect(rowValue("Max remote stream bitrate")).toBe("8 Mbps");
    expect(rowValue("Max local stream bitrate")).toBe("1.5 Mbps (override)");
  });
});

describe("AdminUserDetail effective values", () => {
  it("shows the group-intersected permission set, not the account's assigned one", () => {
    mocks.user = {
      ...adminUser,
      permissions: [PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION],
      effective_policy: {
        ...adminUser.effective_policy,
        permissions: [PERMISSION_MARKER_EDIT],
      },
    };
    renderUserDetail();

    expect(rowValue("Marker Editing")).toBe("Allowed");
    expect(rowValue("Metadata Curation")).toBe("Not allowed");
  });

  it("reports audio transcoding even when video transcoding is allowed", () => {
    mocks.user = {
      ...adminUser,
      effective_policy: {
        ...adminUser.effective_policy,
        transcode_allowed: true,
        audio_transcode_allowed: false,
      },
    };
    renderUserDetail();

    expect(rowValue("Audio Transcodes")).toBe("Not allowed");
  });
});

/** Reads the value rendered next to a label in the effective-values panel. */
function rowValue(label: string): string | undefined {
  return screen.getByText(label).nextElementSibling?.textContent ?? undefined;
}

it("sends a temporary password when the admin requires a change", async () => {
  const user = userEvent.setup();
  mocks.updateUserMutate.mockResolvedValue(undefined);
  renderUserDetail();
  await user.click(screen.getByRole("button", { name: /edit/i }));
  const dialog = await screen.findByRole("dialog");
  const toggle = within(dialog).getByRole("switch", { name: "Require change at next sign-in" });
  expect(toggle).toBeDisabled();
  await user.type(
    within(dialog).getByLabelText("Password (leave blank to keep current)"),
    "temporary-pass",
  );
  await user.click(toggle);
  await user.click(within(dialog).getByRole("button", { name: "Save" }));
  await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalledTimes(1));
  expect(mocks.updateUserMutate.mock.calls[0]![0].body).toMatchObject({
    password: "temporary-pass",
    require_password_change: true,
  });
});

it("hides password actions for an account an external provider manages", async () => {
  const user = userEvent.setup();
  mocks.user = { ...adminUser, password_login: false };
  renderUserDetail();
  expect(screen.queryByRole("button", { name: /reset password/i })).toBeNull();
  await user.click(screen.getByRole("button", { name: /edit/i }));
  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).queryByLabelText(/Password \(leave blank/)).toBeNull();
  expect(within(dialog).getByText(/external sign-in provider/)).toBeInTheDocument();
});

it("offers a password reset for an account that signs in with a password", async () => {
  const user = userEvent.setup();
  renderUserDetail();
  await user.click(screen.getByRole("button", { name: /reset password/i }));
  expect(await screen.findByRole("dialog", { name: "Reset password" })).toBeInTheDocument();
});

it("preserves user draft and frozen guard after conflict until explicit reload", async () => {
  const user = userEvent.setup();
  mocks.updateUserMutate
    .mockRejectedValueOnce(
      new V2ProblemError(
        "updateAdminUser",
        {
          type: "https://silo.example/problems/precondition_failed",
          title: "Changed",
          status: 412,
          detail: "The user changed",
          instance: "/api/v2/admin/users/7",
        },
        null,
        '"do-not-adopt"',
      ),
    )
    .mockResolvedValue(undefined);
  renderUserDetail();
  await user.click(screen.getByRole("button", { name: /edit/i }));
  const dialog = await screen.findByRole("dialog");
  const inputs = within(dialog).getAllByRole("textbox");
  const username = inputs[0]!;
  await user.clear(username);
  await user.type(username, "My draft");
  await user.click(within(dialog).getByRole("button", { name: "Save" }));
  await screen.findByText(/Your draft is preserved/);
  expect(username).toHaveValue("My draft");
  expect(within(dialog).getByRole("button", { name: "Save" })).toBeDisabled();
  expect(mocks.getReads).toBe(1);
  expect(mocks.updateUserMutate.mock.calls[0]![0].editor.etag).toBe('"read-1"');
  await user.click(within(dialog).getByRole("button", { name: "Reload current user" }));
  await waitFor(() => expect(within(dialog).getByRole("button", { name: "Save" })).toBeEnabled());
  expect(username).toHaveValue("My draft");
  await user.click(within(dialog).getByRole("button", { name: "Save" }));
  await waitFor(() => expect(mocks.updateUserMutate).toHaveBeenCalledTimes(2));
  expect(mocks.updateUserMutate.mock.calls[1]![0]).toMatchObject({
    editor: { etag: '"read-2"' },
    body: { username: "My draft" },
  });
});

it("does not install an impersonation session after the captured profile changes", async () => {
  const user = userEvent.setup();
  let finish!: (value: unknown) => void;
  mocks.impersonate.mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  renderUserDetail();
  await user.click(screen.getByRole("button", { name: "View as user" }));
  await user.click(
    within(screen.getByRole("alertdialog")).getByRole("button", { name: "View as user" }),
  );
  const captured = mocks.impersonate.mock.calls[0]![0].profileContext;
  setProfileId("different-profile");
  finish({ session: {}, profileContext: captured });
  await screen.findByText(/account or server changed/);
  expect(mocks.beginImpersonation).not.toHaveBeenCalled();
  expect(mocks.impersonate).toHaveBeenCalledTimes(1);
});

it.each([
  { role: "admin" as const, enabled: true },
  { role: "user" as const, enabled: false },
])("keeps View as user disabled for an ineligible account: %o", (eligibility) => {
  mocks.user = { ...adminUser, ...eligibility };
  renderUserDetail();
  expect(screen.getByRole("button", { name: "View as user" })).toBeDisabled();
});

function userProblem(status: number) {
  return new V2ProblemError("getAdminUser", {
    type: `https://silo.example/problems/${status === 404 ? "not_found" : "internal_error"}`,
    title: status === 404 ? "Not Found" : "Internal Server Error",
    status,
    detail: status === 404 ? "User not found" : "Users are unavailable",
    instance: "/api/v2/admin/users/7",
  });
}

it("points a missing account back to the user list", () => {
  mocks.user = null;
  mocks.userError = userProblem(404);
  renderUserDetail();
  expect(screen.getByRole("heading", { level: 1, name: "User not found" })).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "All users" })).toHaveAttribute("href", "/admin/users");
});

it("offers a retry instead of calling a failed account read missing", async () => {
  mocks.user = null;
  mocks.userError = userProblem(500);
  renderUserDetail();
  expect(
    screen.getByRole("heading", { level: 1, name: "Couldn't load this user" }),
  ).toBeInTheDocument();
  expect(screen.queryByText("User not found")).not.toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Try again" }));
  expect(mocks.refetchUser).toHaveBeenCalledTimes(1);
});

it("keeps showing a loaded account when a background read fails", () => {
  mocks.userError = userProblem(500);
  renderUserDetail();
  expect(
    screen.queryByRole("heading", { name: "Couldn't load this user" }),
  ).not.toBeInTheDocument();
  expect(screen.queryByText("User not found")).not.toBeInTheDocument();
});

it("asks for a profile instead of calling an unread account missing", () => {
  mocks.user = null;
  renderUserDetail();
  expect(
    screen.getByRole("heading", { level: 1, name: "Choose a profile first" }),
  ).toBeInTheDocument();
  // Choosing a profile returns to this account, as the profile guard does.
  expect(screen.getByRole("link", { name: "Choose profile" })).toHaveAttribute(
    "href",
    "/profiles?redirect=%2Fadmin%2Fusers%2F7",
  );
  expect(screen.queryByText("User not found")).not.toBeInTheDocument();
});

it("lets a 404 from a background read replace a loaded account", () => {
  mocks.userError = userProblem(404);
  renderUserDetail();
  expect(screen.getByRole("heading", { level: 1, name: "User not found" })).toBeInTheDocument();
});

describe("AdminUserDetail server owner", () => {
  it("keeps another admin from changing the owner's account", async () => {
    mocks.user = { ...adminUser, role: "admin", is_owner: true };
    mocks.viewer = { id: 99 };
    renderUserDetail();
    expect(await screen.findByText("Owner")).toBeInTheDocument();
    expect(screen.getByText(/Only the owner can change this account/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Edit/ })).toBeDisabled();
    expect(screen.getByRole("button", { name: "View as user" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: /Reset password/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
  });

  it("lets the owner view as another admin", async () => {
    mocks.user = { ...adminUser, role: "admin" };
    mocks.viewerIsOwner = true;
    renderUserDetail();
    expect(await screen.findByRole("button", { name: "View as user" })).toBeEnabled();
  });
});
