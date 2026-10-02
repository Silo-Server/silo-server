// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  captureProfileRequestContext,
  setAccessToken,
  setProfileId,
  setProfileToken,
} from "@/api/client";
import type { AdminUser, UpdateUserRequest } from "@/api/types";
import { V2ProblemError } from "@/api/v2/request";
import { PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION } from "@/lib/permissions";

import AdminUserDetail from "./AdminUserDetail";

interface UpdateArg {
  editor: { etag: string; user: { id: number } };
  body: UpdateUserRequest;
}

const mocks = vi.hoisted(() => ({
  /** The signed-in account and whether it is the server owner. */
  viewer: { id: 1 } as { id: number } | null,
  viewerIsOwner: false,
  available: true,
  update: vi.fn(),
  reads: 0,
  impersonate: vi.fn(),
  transfer: vi.fn(),
  beginImpersonation: vi.fn(),
  /** The account the detail page renders, reset per test. */
  user: null as AdminUser | null,
  userError: null as Error | null,
  refetchUser: vi.fn(),
  live: [] as unknown[],
  setSignIn: vi.fn(),
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
  break_glass: false,
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
    download_transcode_allowed: false,
    requests_allowed: true,
    permissions: [],
  },
  created_at: "2026-07-01T12:00:00Z",
  updated_at: "2026-07-01T12:00:00Z",
};

vi.mock("@/api/v2/adminUsers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/adminUsers")>()),
  getAdminUser: async () => ({
    user: mocks.user!,
    etag: `"read-${++mocks.reads}"`,
    profileContext: (await import("@/api/client")).captureProfileRequestContext()!,
  }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({
  useViewerIsOwner: () => mocks.viewerIsOwner,
  useTransferOwnership: () => ({ mutate: mocks.transfer, isPending: false }),
  useAdminUserCapabilities: () => ({
    data: {
      available: mocks.available,
      default_profile: true,
      ownership_transfer: true,
      password_reset_email: true,
      password_reset_link: true,
      account_downloads: false,
      request_usage: false,
      watch_summary: false,
      account_devices: false,
    },
  }),
  useAdminUser: () => ({
    data: mocks.user ?? undefined,
    editor: mocks.user
      ? { user: mocks.user, etag: '"cached"', profileContext: captureProfileRequestContext() }
      : undefined,
    isLoading: false,
    isFetching: false,
    error: mocks.userError,
    refetch: mocks.refetchUser,
  }),
  useUpdateUser: () => ({ mutateAsync: mocks.update, isPending: false }),
  useDeleteUser: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useImpersonateUser: () => ({ mutateAsync: mocks.impersonate, reset: vi.fn(), isPending: false }),
  useIssuePasswordReset: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useAdminUserSettings: () => ({ data: [], isLoading: false, isError: false }),
  useAdminUserDeviceSettings: () => ({ data: [], isLoading: false, isError: false }),
  useAdminUserSettingCounts: () => ({ account: 0, device: 0, isLoading: false, isError: false }),
}));
vi.mock("@/hooks/queries/admin/userActivity", () => ({
  useAdminUserLiveSessions: () => ({ data: mocks.live, isError: false }),
  useAdminUserDownloadSummary: () => ({ data: undefined }),
  useAdminUserRequestUsage: () => ({ data: undefined }),
}));
vi.mock("@/hooks/queries/admin/accessGroups", () => ({
  useAccessGroups: () => ({
    data: [
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
        allowed_permissions: [PERMISSION_MARKER_EDIT],
        requests_allowed: false,
        is_default: false,
        member_count: 0,
        created_at: "2026-07-01T12:00:00Z",
        updated_at: "2026-07-01T12:00:00Z",
      },
    ],
  }),
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({ useAdminLibraries: () => ({ data: [] }) }));
vi.mock("@/hooks/queries/admin/history", () => ({
  useAdminUserProfiles: () => ({
    data: [
      { id: "p1", name: "Main" },
      { id: "p2", name: "Kids" },
    ],
  }),
}));
vi.mock("@/hooks/queries/admin/requests", () => ({
  useRequestSettings: () => ({
    data: {
      requests_enabled: true,
      global_max_requests: 50,
      global_window_days: 7,
      global_auto_approval_enabled: true,
    },
    isError: false,
    refetch: vi.fn(),
  }),
  useRequestUserLimit: () => ({
    data: { user_id: 7, limit_mode: "inherit", approval_mode: "inherit", etag: '"l"' },
    isError: false,
    refetch: vi.fn(),
  }),
  useRequestGroupLimit: () => ({ data: undefined, isError: false, refetch: vi.fn() }),
  useUpdateRequestUserLimit: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/queries/admin/externalSignIn", () => ({
  useUpdateAdminUserSignIn: () => ({ mutate: mocks.setSignIn, isPending: false }),
  useExternalSignInCapabilities: () => ({
    data: { available: true, admin_identities: true, break_glass: true, provider_recheck: true },
  }),
  useAdminUserIdentities: () => ({ data: [], isLoading: false, isError: false }),
  useUnlinkAdminUserIdentity: () => ({ mutate: vi.fn(), isPending: false }),
  useLinkAdminUserIdentity: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({ data: [], isLoading: false }),
}));
vi.mock("@/hooks/queries/admin/settings", () => ({
  useAdminSettingValue: () => ({ data: "true" }),
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ beginImpersonation: mocks.beginImpersonation, user: mocks.viewer }),
}));
// The other tabs have their own tests.
vi.mock("./admin-users/detail/overview/OverviewTab", () => ({
  OverviewTab: () => <p>Overview content</p>,
}));
vi.mock("./admin-users/detail/activity/ActivityTab", () => ({
  ActivityTab: () => <p>Activity content</p>,
}));
vi.mock("./admin-users/detail/downloads/DownloadsTab", () => ({
  DownloadsTab: () => <p>Downloads content</p>,
}));
vi.mock("./admin-users/detail/preferences/PreferencesTab", () => ({
  PreferencesTab: () => <p>Preferences content</p>,
}));

let router: ReturnType<typeof createMemoryRouter>;

function renderUserDetail(path = "/admin/users/7") {
  router = createMemoryRouter(
    [
      { path: "/admin/users/:id", element: <AdminUserDetail /> },
      { path: "/admin/users", element: <p>All accounts</p> },
    ],
    { initialEntries: [path] },
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
}
const search = () => router.state.location.search;
type Ui = ReturnType<typeof userEvent.setup>;

async function openMenu(ui: Ui) {
  await ui.click(screen.getByRole("button", { name: "More actions" }));
  return screen.findByRole("menu");
}
function menuItems(menu: HTMLElement) {
  return within(menu)
    .getAllByRole("menuitem")
    .map((item) => item.textContent);
}
/** The value side of an Access & limits row. */
function rowValue(label: string): string {
  const node = screen.getByText(label, { selector: "div" });
  return node.closest("[class*='justify-between']")?.lastElementChild?.textContent ?? "";
}

beforeEach(() => {
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  Object.assign(Element.prototype, {
    hasPointerCapture: () => false,
    setPointerCapture: () => {},
    releasePointerCapture: () => {},
    scrollIntoView: () => {},
  });
  mocks.update.mockReset().mockResolvedValue(undefined);
  mocks.reads = 0;
  mocks.impersonate.mockReset();
  mocks.transfer.mockReset();
  mocks.beginImpersonation.mockReset();
  mocks.user = adminUser;
  mocks.viewer = { id: 1 };
  mocks.viewerIsOwner = false;
  mocks.available = true;
  mocks.userError = null;
  mocks.refetchUser.mockReset();
  mocks.live = [];
  mocks.setSignIn.mockReset();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
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

describe("page states", () => {
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
    expect(screen.getByRole("heading", { level: 1, name: "taylor" })).toBeInTheDocument();
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
  });

  it("lets a 404 from a background read replace a loaded account", () => {
    mocks.userError = userProblem(404);
    renderUserDetail();
    expect(screen.getByRole("heading", { level: 1, name: "User not found" })).toBeInTheDocument();
  });

  it("disables every action while user administration is unavailable", () => {
    mocks.available = false;
    renderUserDetail("/admin/users/7?tab=access");
    expect(screen.getByRole("status")).toHaveTextContent("User administration is unavailable.");
    expect(screen.getByRole("button", { name: "View as user" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reset password" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit Playback & streaming" })).toBeDisabled();
  });
});

describe("header", () => {
  it("shows who the account is", () => {
    mocks.user = {
      ...adminUser,
      access_group_id: 5,
      password_change_required: true,
      last_active_at: undefined,
    };
    renderUserDetail();
    expect(screen.getByRole("heading", { level: 1, name: "taylor" })).toBeInTheDocument();
    expect(screen.getByText("User")).toBeInTheDocument();
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByText("Temporary password")).toBeInTheDocument();
    const meta = screen.getByText("taylor@example.test").parentElement!;
    expect(meta).toHaveTextContent("Group: Guests");
    expect(meta).toHaveTextContent("No recorded activity");
    expect(meta).toHaveTextContent("Password sign-in");
  });

  it("names the month of an old last activity, like the page's other dates", () => {
    mocks.user = { ...adminUser, last_active_at: "2020-08-30T12:00:00Z" };
    renderUserDetail();
    const meta = screen.getByText(adminUser.email!).parentElement!;
    expect(meta).toHaveTextContent(/Last active (Aug 30, 2020|30 Aug 2020)/);
  });

  it("hides password actions for an account an external provider manages", () => {
    mocks.user = { ...adminUser, password_login: false };
    renderUserDetail("/admin/users/7?tab=access");
    expect(screen.queryByRole("button", { name: /reset password/i })).toBeNull();
    expect(screen.getByText("External sign-in")).toBeInTheDocument();
    expect(rowValue("Password")).toBe("Managed by an external sign-in provider");
  });

  it("sets a password for an account without password sign-in from its Sign-in tab", async () => {
    const ui = userEvent.setup();
    mocks.user = { ...adminUser, password_login: false };
    renderUserDetail();
    // A reset link needs password sign-in on; a set password turns it back on.
    expect(screen.queryByRole("button", { name: /reset password/i })).toBeNull();
    // The header button opens the Sign-in tab with its Set password dialog.
    await ui.click(screen.getByRole("button", { name: "Set password" }));
    let dialog = await screen.findByRole("dialog", { name: "Set a password" });
    expect(search()).toBe("?tab=sign-in");
    // Canceling closes it on the Sign-in tab, whose own button opens it again.
    await ui.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("tab", { name: "Sign-in" })).toHaveAttribute("aria-selected", "true");
    await ui.click(await screen.findByRole("button", { name: "Set a password" }));
    dialog = await screen.findByRole("dialog", { name: "Set a password" });
    await ui.type(within(dialog).getByLabelText("New password"), "recovered-pass");
    await ui.click(within(dialog).getByRole("button", { name: "Set password" }));
    expect(mocks.setSignIn).toHaveBeenCalledWith(
      { userId: 7, body: { password: "recovered-pass", require_password_change: true } },
      expect.anything(),
    );
  });

  it("offers a password reset for an account that signs in with a password", async () => {
    const ui = userEvent.setup();
    renderUserDetail();
    await ui.click(screen.getByRole("button", { name: "Reset password" }));
    expect(
      await screen.findByRole("dialog", { name: "Reset password for taylor" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Set a temporary password" })).toBeInTheDocument();
  });

  it("shows a disabled account's banner and enables it with a fresh validator", async () => {
    const ui = userEvent.setup();
    mocks.user = { ...adminUser, enabled: false };
    renderUserDetail();
    expect(screen.getByText("Disabled")).toBeInTheDocument();
    expect(
      screen.getByText("This account can't sign in. Its profiles and history are kept."),
    ).toBeInTheDocument();
    // A temporary password can still be set while the account is disabled.
    expect(screen.getByRole("button", { name: "Reset password" })).toBeEnabled();
    await ui.click(screen.getByRole("button", { name: "Enable account" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    const call = mocks.update.mock.calls[0]![0] as UpdateArg;
    expect(call.body).toEqual({ enabled: true });
    expect(call.editor.etag).toBe('"read-1"');
  });

  it.each([
    { role: "admin" as const, enabled: true },
    { role: "user" as const, enabled: false },
  ])("keeps View as user disabled for an ineligible account: %o", (eligibility) => {
    mocks.user = { ...adminUser, ...eligibility };
    renderUserDetail();
    expect(screen.getByRole("button", { name: "View as user" })).toBeDisabled();
  });

  it("does not install an impersonation session after the captured profile changes", async () => {
    const ui = userEvent.setup();
    let finish!: (value: unknown) => void;
    mocks.impersonate.mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    renderUserDetail();
    await ui.click(screen.getByRole("button", { name: "View as user" }));
    await ui.click(
      within(screen.getByRole("alertdialog")).getByRole("button", { name: "View as user" }),
    );
    const captured = mocks.impersonate.mock.calls[0]![0].profileContext;
    setProfileId("different-profile");
    await act(async () => finish({ session: {}, profileContext: captured }));
    await screen.findByText(/account or server changed/);
    expect(mocks.beginImpersonation).not.toHaveBeenCalled();
    expect(mocks.impersonate).toHaveBeenCalledTimes(1);
  });
});

describe("more actions", () => {
  it("offers disable and delete for a user an admin manages", async () => {
    const ui = userEvent.setup();
    renderUserDetail();
    const menu = await openMenu(ui);
    expect(menuItems(menu)).toEqual(["Disable accountkeeps data", "Delete account…"]);
  });

  it("disables an account after confirming", async () => {
    const ui = userEvent.setup();
    renderUserDetail();
    await ui.click(within(await openMenu(ui)).getByRole("menuitem", { name: /Disable account/ }));
    const confirm = await screen.findByRole("alertdialog", { name: "Disable taylor?" });
    expect(confirm).toHaveTextContent("Their profiles, history, and downloads are kept.");
    await ui.click(within(confirm).getByRole("button", { name: "Disable account" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    expect((mocks.update.mock.calls[0]![0] as UpdateArg).body).toEqual({ enabled: false });
  });

  it("reports a changed account instead of disabling it", async () => {
    const ui = userEvent.setup();
    mocks.update.mockRejectedValue(
      new V2ProblemError("updateAdminUser", {
        type: "https://silo.example/problems/precondition_failed",
        title: "Changed",
        status: 412,
        detail: "Changed",
        instance: "/api/v2/admin/users/7",
      }),
    );
    renderUserDetail();
    await ui.click(within(await openMenu(ui)).getByRole("menuitem", { name: /Disable account/ }));
    await ui.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", {
        name: "Disable account",
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("The account changed. Try again.");
  });

  it("confirms a delete by name and counts the profiles", async () => {
    const ui = userEvent.setup();
    renderUserDetail();
    await ui.click(within(await openMenu(ui)).getByRole("menuitem", { name: "Delete account…" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Delete taylor?" });
    expect(dialog).toHaveTextContent("its 2 profiles, watch history, and saved preferences");
    expect(within(dialog).getByRole("button", { name: "Delete account" })).toBeDisabled();
    expect(within(dialog).getByRole("button", { name: "Disable instead" })).toBeInTheDocument();
  });

  it("offers Enable instead of Disable for a disabled account", async () => {
    const ui = userEvent.setup();
    mocks.user = { ...adminUser, enabled: false };
    renderUserDetail();
    expect(menuItems(await openMenu(ui))).toEqual(["Enable account", "Delete account…"]);
  });
});

describe("server owner", () => {
  it("keeps another admin from changing the owner's account", () => {
    mocks.user = { ...adminUser, role: "admin", is_owner: true };
    mocks.viewer = { id: 99 };
    renderUserDetail("/admin/users/7?tab=access");
    // The header badge and the Role row both name the owner.
    expect(screen.getAllByText("Owner")).toHaveLength(2);
    expect(rowValue("Role")).toBe("Owner");
    expect(
      screen.getByText("This is the server owner. Only the owner can change this account."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "View as user" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: /Reset password/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "More actions" })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Edit / })).toBeNull();
    expect(screen.getAllByText("View only")).toHaveLength(5);
  });

  it("keeps an admin other than the owner from changing another admin", () => {
    mocks.user = { ...adminUser, role: "admin" };
    mocks.viewer = { id: 99 };
    renderUserDetail();
    expect(
      screen.getByText("Only the server owner can change another admin account. You can view it."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Reset password/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "More actions" })).toBeNull();
  });

  it("lets the owner make another enabled admin the owner", async () => {
    const ui = userEvent.setup();
    mocks.user = { ...adminUser, role: "admin" };
    mocks.viewerIsOwner = true;
    renderUserDetail();
    const menu = await openMenu(ui);
    expect(menuItems(menu)).toEqual([
      "Make ownerowner only",
      "Disable accountkeeps data",
      "Delete account…",
    ]);
    await ui.click(within(menu).getByRole("menuitem", { name: /Make owner/ }));
    await ui.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Make owner" }),
    );
    expect(mocks.transfer).toHaveBeenCalledWith(
      expect.objectContaining({ id: adminUser.id }),
      expect.anything(),
    );
    // Confirming closes the dialog, so a stale confirmation cannot send a
    // second transfer.
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
  });

  it("offers ownership only for an enabled admin", async () => {
    const ui = userEvent.setup();
    mocks.viewerIsOwner = true;
    mocks.user = { ...adminUser, role: "user" };
    renderUserDetail();
    expect(menuItems(await openMenu(ui))).not.toContain("Make ownerowner only");
    cleanup();
    mocks.user = { ...adminUser, role: "admin", enabled: false };
    renderUserDetail();
    expect(menuItems(await openMenu(ui))).toEqual(["Enable account", "Delete account…"]);
  });

  it("offers no Delete or Disable on the viewer's own account", () => {
    mocks.user = { ...adminUser, role: "admin" };
    mocks.viewer = { id: adminUser.id };
    renderUserDetail("/admin/users/7?tab=access");
    expect(screen.queryByRole("button", { name: "More actions" })).toBeNull();
    expect(screen.getByRole("button", { name: "Edit Sign-in & role" })).toBeEnabled();
  });

  it("lets the owner view as another admin", () => {
    mocks.user = { ...adminUser, role: "admin" };
    mocks.viewerIsOwner = true;
    renderUserDetail();
    expect(screen.getByRole("button", { name: "View as user" })).toBeEnabled();
  });
});

describe("tabs", () => {
  it("opens the tab the URL names and writes the tab it switches to", async () => {
    const ui = userEvent.setup();
    renderUserDetail("/admin/users/7?tab=access");
    expect(screen.getByRole("tab", { name: /Access & limits/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    expect(screen.getByRole("region", { name: "Playback & streaming" })).toBeInTheDocument();
    await ui.click(screen.getByRole("tab", { name: /Downloads/ }));
    expect(search()).toBe("?tab=downloads");
    expect(screen.getByText("Downloads content")).toBeInTheDocument();
    await ui.click(screen.getByRole("tab", { name: "Overview" }));
    expect(search()).toBe("");
    expect(screen.getByText("Overview content")).toBeInTheDocument();
  });

  it("opens Overview for a missing or unknown tab", () => {
    renderUserDetail("/admin/users/7?tab=settings");
    expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Overview content")).toBeInTheDocument();
  });

  it("counts custom limits and shows a dot while the account watches", () => {
    mocks.user = { ...adminUser, max_streams: 2, transcode_allowed: true, max_transcodes: 1 };
    mocks.live = [{ session_id: "s1", profile_id: "p1" }];
    renderUserDetail();
    expect(screen.getByRole("tab", { name: /Access & limits/ })).toHaveTextContent(
      "Access & limits2 custom",
    );
    expect(
      within(screen.getByRole("tab", { name: /Activity/ })).getByRole("img", {
        name: "Watching now",
      }),
    ).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: /Preferences/ })).toHaveTextContent("Preferences0");
  });
});

describe("Sign-in tab", () => {
  it("shows the account's sign-in", async () => {
    mocks.user = { ...adminUser, role: "admin", break_glass: true };
    mocks.viewerIsOwner = true;
    renderUserDetail("/admin/users/7?tab=sign-in");
    expect(
      await screen.findByText("On: taylor can sign in with a Silo password."),
    ).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "Break-glass account" })).toBeChecked();
    expect(screen.getByText("Not connected to a sign-in provider.")).toBeInTheDocument();
  });
});

describe("Access & limits in the page", () => {
  it("shows the group-intersected permission set, not the account's assigned one", () => {
    mocks.user = {
      ...adminUser,
      permissions: [PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION],
      effective_policy: { ...adminUser.effective_policy, permissions: [PERMISSION_MARKER_EDIT] },
    };
    renderUserDetail("/admin/users/7?tab=access");
    expect(rowValue("Marker editing")).toBe("Allowed");
    expect(rowValue("Metadata curation")).toBe("Not allowed");
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
    renderUserDetail("/admin/users/7?tab=access");
    expect(rowValue("Video transcoding")).toBe("UnlimitedDEFAULT");
    expect(rowValue("Audio-only transcoding")).toBe("Not allowedDEFAULT");
  });

  it("shows the Requests card with the account's switch", () => {
    renderUserDetail("/admin/users/7?tab=access");
    const requests = screen.getByRole("region", { name: "Requests" });
    expect(within(requests).getByRole("link", { name: /Requests/ })).toHaveAttribute(
      "href",
      "/admin/requests?user=7",
    );
    expect(rowValue("Can request media")).toBe("YesDEFAULT");
  });
});

describe("unsaved changes", () => {
  async function dirtyPlayback(ui: Ui) {
    renderUserDetail("/admin/users/7?tab=access");
    await ui.click(screen.getByRole("button", { name: "Edit Playback & streaming" }));
    const playback = screen.getByRole("region", { name: "Playback & streaming" });
    await ui.click(
      within(within(playback).getByRole("group", { name: "Simultaneous streams" })).getByRole(
        "button",
        { name: "Custom" },
      ),
    );
    const streams = within(playback).getByRole("spinbutton", { name: "Simultaneous streams" });
    await ui.clear(streams);
    await ui.type(streams, "2");
    return playback;
  }

  it("disables Delete until the card's changes are saved or cancelled", async () => {
    const ui = userEvent.setup();
    await dirtyPlayback(ui);
    const menu = await openMenu(ui);
    expect(within(menu).getByRole("menuitem", { name: /Delete account/ })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(menu).toHaveTextContent("save or cancel edits first");
  });

  it("asks before switching tabs, and Keep editing stays", async () => {
    const ui = userEvent.setup();
    const playback = await dirtyPlayback(ui);
    await ui.click(screen.getByRole("tab", { name: "Overview" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Leave without saving?" });
    expect(dialog).toHaveTextContent("You have 1 unsaved change in Playback & streaming.");
    await ui.click(within(dialog).getByRole("button", { name: "Keep editing" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(search()).toBe("?tab=access");
    expect(within(playback).getByRole("spinbutton", { name: "Simultaneous streams" })).toHaveValue(
      2,
    );
  });

  it("discards the draft and moves on", async () => {
    const ui = userEvent.setup();
    await dirtyPlayback(ui);
    await ui.click(screen.getByRole("tab", { name: /Activity/ }));
    await ui.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Discard" }),
    );
    await waitFor(() => expect(search()).toBe("?tab=activity"));
    expect(screen.getByText("Activity content")).toBeInTheDocument();
    expect(mocks.update).not.toHaveBeenCalled();
  });

  it("saves the draft, then moves on", async () => {
    const ui = userEvent.setup();
    await dirtyPlayback(ui);
    await ui.click(screen.getByRole("tab", { name: "Overview" }));
    await ui.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", {
        name: "Save and continue",
      }),
    );
    await waitFor(() => expect(search()).toBe(""));
    expect(mocks.update).toHaveBeenCalledTimes(1);
    expect((mocks.update.mock.calls[0]![0] as UpdateArg).body).toEqual({ max_streams: 2 });
    expect((mocks.update.mock.calls[0]![0] as UpdateArg).editor.etag).toBe('"cached"');
  });

  it("stays when the save is refused, with the conflict in the card", async () => {
    const ui = userEvent.setup();
    mocks.update.mockRejectedValue(
      new V2ProblemError("updateAdminUser", {
        type: "https://silo.example/problems/precondition_failed",
        title: "Changed",
        status: 412,
        detail: "Changed",
        instance: "/api/v2/admin/users/7",
      }),
    );
    const playback = await dirtyPlayback(ui);
    await ui.click(screen.getByRole("tab", { name: "Overview" }));
    await ui.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", {
        name: "Save and continue",
      }),
    );
    expect(
      await within(playback).findByText(/Another admin changed this account after you started/),
    ).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(search()).toBe("?tab=access");
  });

  it("asks before leaving the page too", async () => {
    const ui = userEvent.setup();
    await dirtyPlayback(ui);
    await ui.click(screen.getByRole("link", { name: "Users" }));
    await ui.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Discard" }),
    );
    expect(await screen.findByText("All accounts")).toBeInTheDocument();
  });
});
