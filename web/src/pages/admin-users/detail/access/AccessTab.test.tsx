// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AccessGroup, AdminUser, Library, UpdateUserRequest } from "@/api/types";
import type { AdminUserEditor } from "@/api/v2/adminUsers";
import { V2ProblemError } from "@/api/v2/request";
import { PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION } from "@/lib/permissions";

import { CardEditingProvider } from "../cardEditing";
import { AccessTab } from "./AccessTab";
import { POLICY_DEFAULTS } from "@/test/policyDefaults";

const mocks = vi.hoisted(() => ({
  viewer: { id: 1 } as { id: number },
  viewerIsOwner: false,
  update: vi.fn(),
  reads: 0,
  user: null as AdminUser | null,
}));

const CONTEXT = { profileId: "owner" } as unknown as ProfileRequestContextSnapshot;

vi.mock("@/api/v2/adminUsers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/adminUsers")>()),
  getAdminUser: async () => ({
    user: mocks.user!,
    etag: `"read-${++mocks.reads}"`,
    profileContext: CONTEXT,
  }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminPolicyDefaults: () => ({ data: POLICY_DEFAULTS }),
  useViewerIsOwner: () => mocks.viewerIsOwner,
  useAdminUserCapabilities: () => ({
    data: { available: true, account_downloads: true, request_usage: false },
  }),
  useUpdateUser: () => ({ mutateAsync: mocks.update, isPending: false }),
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: mocks.viewer }) }));
vi.mock("@/hooks/queries/admin/history", () => ({
  useAdminUserProfiles: () => ({ data: [{ id: "p1", name: "Main" }] }),
}));
vi.mock("@/hooks/queries/admin/userActivity", () => ({
  useAdminUserRequestUsage: () => ({ data: undefined }),
}));
vi.mock("@/hooks/queries/admin/requests", () => ({
  useRequestSettings: () => ({
    data: {
      requests_enabled: true,
      global_max_requests: 50,
      global_window_days: 7,
      global_auto_approval_enabled: true,
      force_dual_quality: false,
      updated_at: "2026-01-01T00:00:00Z",
    },
    isError: false,
    refetch: vi.fn(),
  }),
  useRequestUserLimit: () => ({
    data: { user_id: 7, limit_mode: "inherit", approval_mode: "inherit", etag: '"limit"' },
    isError: false,
    isLoading: false,
    refetch: vi.fn(),
  }),
  useRequestGroupLimit: (groupId?: number | null) => ({
    data: groupId
      ? { group_id: groupId, limit_mode: "inherit", approval_mode: "manual", etag: '"g"' }
      : undefined,
    isError: false,
    refetch: vi.fn(),
  }),
  useUpdateRequestUserLimit: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));

const GROUPS: AccessGroup[] = [
  {
    id: 3,
    name: "Kids",
    description: "",
    library_ids: null,
    max_playback_quality: "",
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
    is_default: false,
    member_count: 0,
    created_at: "2026-07-01T12:00:00Z",
    updated_at: "2026-07-01T12:00:00Z",
  },
  {
    id: 5,
    name: "Guests",
    description: "",
    library_ids: [],
    max_playback_quality: "1080p",
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
];
vi.mock("@/hooks/queries/admin/accessGroups", () => ({
  useAccessGroups: () => ({ data: GROUPS }),
}));
const LIBRARIES = [
  { id: 1, name: "Movies", enabled: true, type: "movies" },
  { id: 2, name: "TV", enabled: true, type: "series" },
] as Library[];
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({ data: LIBRARIES }),
}));

const USER: AdminUser = {
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

/** A user in the Guests group, whose effective policy is the group's. */
const GUEST: AdminUser = {
  ...USER,
  access_group_id: 5,
  effective_policy: {
    ...USER.effective_policy,
    library_ids: [],
    max_playback_quality: "1080p",
    max_streams: 1,
    transcode_allowed: false,
    max_remote_stream_bitrate_kbps: 8000,
    download_allowed: false,
    requests_allowed: false,
  },
};

function mount(
  user: AdminUser = USER,
  {
    manageable = true,
    policyManageable,
  }: { manageable?: boolean; policyManageable?: boolean } = {},
) {
  mocks.user = user;
  const editor: AdminUserEditor = { user, etag: '"cached"', profileContext: CONTEXT };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <CardEditingProvider>
          <AccessTab
            user={user}
            editor={editor}
            manageable={manageable}
            policyManageable={policyManageable ?? manageable}
            available
          />
        </CardEditingProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

type Ui = ReturnType<typeof userEvent.setup>;

function card(title: string): HTMLElement {
  return screen.getByRole("region", { name: title });
}
async function edit(ui: Ui, title: string) {
  await ui.click(screen.getByRole("button", { name: `Edit ${title}` }));
  return card(title);
}
async function pick(ui: Ui, scope: HTMLElement, combobox: string, option: string) {
  await ui.click(within(scope).getByRole("combobox", { name: combobox }));
  await ui.click(await screen.findByRole("option", { name: option }));
}
function segment(scope: HTMLElement, label: string) {
  return within(scope).getByRole("group", { name: label });
}
async function customize(ui: Ui, scope: HTMLElement, label: string) {
  await ui.click(within(segment(scope, label)).getByRole("button", { name: "Custom" }));
}
function lastBody(): UpdateUserRequest {
  const call = mocks.update.mock.calls.at(-1)?.[0] as { body: UpdateUserRequest } | undefined;
  if (!call) throw new Error("no save");
  return call.body;
}
/** The value side of a row in a card: value, the value it replaces, and the tag. */
function row(scope: HTMLElement, label: string): string {
  const labelNode = within(scope).getByText(label, { selector: "div" });
  return labelNode.closest("[class*='justify-between']")?.lastElementChild?.textContent ?? "";
}

const conflict = () =>
  new V2ProblemError("updateAdminUser", {
    type: "https://silo.example/problems/precondition_failed",
    title: "Changed",
    status: 412,
    detail: "The user changed",
    instance: "/api/v2/admin/users/7",
  });

beforeEach(() => {
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
  mocks.viewer = { id: 1 };
  mocks.viewerIsOwner = false;
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("card editing", () => {
  it("edits one card at a time", async () => {
    const ui = userEvent.setup();
    mount();
    const playback = await edit(ui, "Playback & streaming");
    expect(within(playback).getByText("Editing")).toBeInTheDocument();
    for (const title of ["Sign-in & role", "Library access", "Downloads", "Requests"]) {
      expect(screen.getByRole("button", { name: `Edit ${title}` })).toBeDisabled();
    }
    await ui.click(within(playback).getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("button", { name: "Edit Requests" })).toBeEnabled();
  });

  it("shows View only instead of Edit when the account can't be changed", () => {
    mount(USER, { manageable: false });
    expect(screen.queryByRole("button", { name: /^Edit / })).toBeNull();
    expect(within(card("Playback & streaming")).getByText("View only")).toBeInTheDocument();
  });

  it("keeps an admin's access and limits to the owner", () => {
    mount({ ...USER, role: "admin" }, { policyManageable: false });
    expect(
      screen.getByText("Only the server owner can change an admin's access and limits."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit Sign-in & role" })).toBeInTheDocument();
    for (const title of ["Library access", "Downloads", "Playback & streaming", "Requests"]) {
      expect(within(card(title)).getByText("View only")).toBeInTheDocument();
    }
  });

  it("saves one PUT with only the changed fields and the captured validator", async () => {
    const ui = userEvent.setup();
    mount();
    const playback = await edit(ui, "Playback & streaming");
    const save = within(playback).getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();
    expect(within(playback).getByText("0 unsaved changes")).toBeInTheDocument();

    await customize(ui, playback, "Simultaneous streams");
    const streams = within(playback).getByRole("spinbutton", { name: "Simultaneous streams" });
    // The server default is unlimited, so Custom starts at 0.
    expect(streams).toHaveValue(0);
    await ui.clear(streams);
    await ui.type(streams, "2");
    expect(
      within(playback).getByText("1 unsaved change · Simultaneous streams"),
    ).toBeInTheDocument();
    expect(streams.closest("[data-changed='true']")).not.toBeNull();
    await ui.click(save);

    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    expect(mocks.update.mock.calls[0]![0]).toMatchObject({ editor: { etag: '"cached"' } });
    expect(lastBody()).toEqual({ max_streams: 2 });
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Edit Playback & streaming" })).toBeEnabled(),
    );
  });

  it("keeps the draft after a 412 and waits for Reload and review", async () => {
    const ui = userEvent.setup();
    mocks.update.mockRejectedValueOnce(conflict()).mockResolvedValue(undefined);
    mount();
    const signIn = await edit(ui, "Sign-in & role");
    const username = within(signIn).getByLabelText("Username");
    await ui.clear(username);
    await ui.type(username, "My draft");
    await ui.click(within(signIn).getByRole("button", { name: "Save" }));

    expect(
      await within(signIn).findByText(
        "Another admin changed this account after you started. Your edits are kept.",
      ),
    ).toBeInTheDocument();
    expect(username).toHaveValue("My draft");
    expect(within(signIn).getByRole("button", { name: "Save" })).toBeDisabled();

    // Meanwhile another admin changed the email, a field this draft never touched.
    mocks.user = { ...USER, email: "other-admin@example.test" };
    await ui.click(within(signIn).getByRole("button", { name: "Reload and review" }));
    await waitFor(() => expect(within(signIn).getByRole("button", { name: "Save" })).toBeEnabled());
    expect(username).toHaveValue("My draft");
    // The untouched field follows the newer account instead of reverting it.
    expect(within(signIn).getByLabelText("Email")).toHaveValue("other-admin@example.test");
    expect(within(signIn).getByText("1 unsaved change · Username")).toBeInTheDocument();
    await ui.click(within(signIn).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(2));
    expect(mocks.update.mock.calls[0]![0].editor.etag).toBe('"cached"');
    expect(mocks.update.mock.calls[1]![0]).toMatchObject({
      editor: { etag: '"read-1"' },
      body: { username: "My draft" },
    });
    expect(lastBody()).toEqual({ username: "My draft" });
  });

  it("locks the card's inputs while a save is in flight", async () => {
    const ui = userEvent.setup();
    let finish!: () => void;
    mocks.update.mockImplementationOnce(() => new Promise<void>((done) => (finish = done)));
    mount();
    const signIn = await edit(ui, "Sign-in & role");
    const username = within(signIn).getByLabelText("Username");
    await ui.clear(username);
    await ui.type(username, "Saved name");
    await ui.click(within(signIn).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(username).toBeDisabled());
    expect(within(signIn).getByLabelText("Email")).toBeDisabled();
    finish();
    await waitFor(() => expect(within(signIn).queryByLabelText("Username")).toBeNull());
    expect(lastBody()).toEqual({ username: "Saved name" });
  });

  it("refuses an invalid email before saving", async () => {
    const ui = userEvent.setup();
    mount();
    const signIn = await edit(ui, "Sign-in & role");
    const email = within(signIn).getByLabelText("Email");
    await ui.clear(email);
    await ui.type(email, "not-an-email");
    await ui.click(within(signIn).getByRole("button", { name: "Save" }));
    expect(within(signIn).getByRole("alert")).toHaveTextContent("Enter a valid email address");
    expect(mocks.update).not.toHaveBeenCalled();
  });
});

describe("Sign-in & role", () => {
  it("offers an admin other than the owner no admin role", async () => {
    const ui = userEvent.setup();
    mount();
    const signIn = await edit(ui, "Sign-in & role");
    await ui.click(within(signIn).getByRole("combobox", { name: "Role" }));
    expect(await screen.findByRole("option", { name: "Admin" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(screen.getByText("Only the server owner can grant the admin role.")).toBeInTheDocument();
  });

  it("keeps an admin from changing its own role or disabling itself", async () => {
    const ui = userEvent.setup();
    mocks.viewer = { id: USER.id };
    mount({ ...USER, role: "admin" });
    const signIn = await edit(ui, "Sign-in & role");
    expect(within(signIn).getByRole("combobox", { name: "Role" })).toBeDisabled();
    expect(within(signIn).getByText("You can't change your own role.")).toBeInTheDocument();
    expect(within(signIn).getByRole("switch", { name: "Can sign in" })).toBeDisabled();
    expect(within(signIn).getByText("You can't disable your own account.")).toBeInTheDocument();
  });

  it("clears the group when the owner promotes a grouped account to admin", async () => {
    const ui = userEvent.setup();
    mocks.viewerIsOwner = true;
    mount({ ...USER, access_group_id: 5 });
    const signIn = await edit(ui, "Sign-in & role");
    await pick(ui, signIn, "Role", "Admin");
    expect(
      within(signIn).getByText(
        "Admins don't use access groups. Saving removes this account from Guests.",
      ),
    ).toBeInTheDocument();
    await ui.click(within(signIn).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ role: "admin", access_group_id: null });
  });

  it("names an external provider instead of a password", () => {
    mount({ ...USER, password_login: false });
    expect(row(card("Sign-in & role"), "Password")).toContain(
      "Managed by an external sign-in provider",
    );
  });
});

describe("Library access", () => {
  it("shows the group-intersected permission set, not the account's assigned one", () => {
    mount({
      ...GUEST,
      permissions: [PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION],
      effective_policy: { ...GUEST.effective_policy, permissions: [PERMISSION_MARKER_EDIT] },
    });
    const library = card("Library access");
    expect(row(library, "Marker editing")).toContain("Allowed");
    expect(row(library, "Metadata curation")).toContain("Not allowed");
    expect(within(library).getByText("Blocked by the Guests group")).toBeInTheDocument();
  });

  it("locks a permission the group does not allow", async () => {
    const ui = userEvent.setup();
    mount(GUEST);
    const library = await edit(ui, "Library access");
    expect(within(library).getByRole("switch", { name: "Metadata curation" })).toBeDisabled();
    expect(within(library).getByText("Guests group doesn't allow this")).toBeInTheDocument();
    expect(within(library).getByRole("switch", { name: "Marker editing" })).toBeEnabled();
    expect(within(library).getByText("The Guests group allows this")).toBeInTheDocument();
  });

  it("shows a blocked assignment as off, with a note", async () => {
    const ui = userEvent.setup();
    mount({ ...GUEST, permissions: [PERMISSION_METADATA_CURATION] });
    const library = await edit(ui, "Library access");
    const curation = within(library).getByRole("switch", { name: "Metadata curation" });
    expect(curation).toBeDisabled();
    expect(curation).not.toBeChecked();
    expect(
      within(library).getByText("Set on this account; has no effect under Guests"),
    ).toBeInTheDocument();
  });

  it("removes a blocked assignment from the account", async () => {
    const ui = userEvent.setup();
    mocks.update.mockResolvedValue(undefined);
    mount({ ...GUEST, permissions: [PERMISSION_METADATA_CURATION] });
    const library = await edit(ui, "Library access");
    await ui.click(within(library).getByRole("button", { name: "Remove from account" }));
    await ui.click(within(library).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    expect(lastBody()).toEqual({ permissions: [] });
  });

  it("rebases permission switches onto a reloaded account", async () => {
    const ui = userEvent.setup();
    mocks.update.mockRejectedValueOnce(conflict()).mockResolvedValue(undefined);
    mount();
    const library = await edit(ui, "Library access");
    await ui.click(within(library).getByRole("switch", { name: "Marker editing" }));
    await ui.click(within(library).getByRole("button", { name: "Save" }));
    await within(library).findByText(/Another admin changed this account/);

    mocks.user = { ...USER, permissions: [PERMISSION_METADATA_CURATION] };
    await ui.click(within(library).getByRole("button", { name: "Reload and review" }));
    await waitFor(() =>
      expect(within(library).getByRole("button", { name: "Save" })).toBeEnabled(),
    );
    expect(within(library).getByRole("switch", { name: "Metadata curation" })).toBeChecked();
    await ui.click(within(library).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(2));
    expect(lastBody()).toEqual({
      permissions: [PERMISSION_MARKER_EDIT, PERMISSION_METADATA_CURATION].sort(),
    });
  });

  it("switches library_ids between the group's set and a chosen list", async () => {
    const ui = userEvent.setup();
    mount({ ...USER, access_group_id: 3 });
    const library = await edit(ui, "Library access");
    const inherit = within(library).getByRole("radio", { name: /Use the group's libraries/ });
    expect(inherit).toBeChecked();
    await ui.click(within(library).getByRole("radio", { name: "Choose for this account" }));
    // Starts from what the account sees now: every library.
    expect(within(library).getByRole("checkbox", { name: "Movies" })).toBeChecked();
    await ui.click(within(library).getByRole("checkbox", { name: "TV" }));
    expect(within(library).getByText("1 unsaved change · Libraries")).toBeInTheDocument();
    await ui.click(within(library).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    expect(lastBody()).toEqual({ library_ids: [1] });

    cleanup();
    mocks.update.mockClear();
    mount({
      ...USER,
      library_ids: [2],
      effective_policy: { ...USER.effective_policy, library_ids: [2] },
    });
    const again = await edit(ui, "Library access");
    await ui.click(within(again).getByRole("radio", { name: /Use the server default/ }));
    await ui.click(within(again).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    expect(lastBody()).toEqual({ library_ids: null });
  });

  it("previews the picked group in this card", async () => {
    const ui = userEvent.setup();
    mount();
    const library = await edit(ui, "Library access");
    expect(
      within(library).getByRole("radio", { name: /Use the server default/ }),
    ).toHaveAccessibleName(/All libraries/);
    await pick(ui, library, "Access group", "Guests");
    expect(
      within(library).getByText(/^Guests: No libraries · 1080p · 1 streams/),
    ).toBeInTheDocument();
    expect(
      within(library).getByRole("radio", { name: /Use the group's libraries/ }),
    ).toHaveAccessibleName(/No libraries/);
    expect(within(library).getByRole("link", { name: /Open group/ })).toHaveAttribute(
      "href",
      "/admin/access-groups/5",
    );
    await ui.click(within(library).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ access_group_id: 5 });
  });

  it("tells admins they don't use groups", () => {
    mount({ ...USER, role: "admin" });
    const library = card("Library access");
    expect(
      within(library).getByText(
        "Admins don't use access groups. Unset values follow the server default.",
      ),
    ).toBeInTheDocument();
    expect(within(library).queryByText("Access group")).toBeNull();
  });
});

describe("sources and inherited values", () => {
  it("tags grouped values and shows what a custom one replaces", () => {
    mount({
      ...GUEST,
      max_streams: 3,
      max_remote_stream_bitrate_kbps: 12000,
      effective_policy: {
        ...GUEST.effective_policy,
        max_streams: 3,
        max_remote_stream_bitrate_kbps: 12000,
      },
    });
    const playback = card("Playback & streaming");
    expect(row(playback, "Simultaneous streams")).toBe("3Group: 1CUSTOM");
    expect(row(playback, "Bitrate cap, remote")).toBe("12 MbpsGroup: 8 MbpsCUSTOM");
    expect(row(playback, "Max quality")).toBe("1080pGROUP");
    expect(
      within(playback).getByText("2 limits set for this account; the rest follow Guests."),
    ).toBeInTheDocument();
  });

  it("uses the saved account's resolved policy when the group list is stale", async () => {
    const ui = userEvent.setup();
    mount({
      ...GUEST,
      effective_policy: { ...GUEST.effective_policy, max_remote_stream_bitrate_kbps: 30_720 },
    });
    const playback = await edit(ui, "Playback & streaming");
    expect(
      within(segment(playback, "Bitrate cap, remote")).getByRole("button", {
        name: "Default · 30.72 Mbps",
      }),
    ).toBeInTheDocument();
  });

  it("says Default without a value when the group is not loaded", async () => {
    const ui = userEvent.setup();
    mount({
      ...USER,
      access_group_id: 99,
      max_remote_stream_bitrate_kbps: 2000,
      effective_policy: { ...USER.effective_policy, max_remote_stream_bitrate_kbps: 2000 },
    });
    expect(row(card("Playback & streaming"), "Bitrate cap, remote")).not.toContain("Group:");
    const playback = await edit(ui, "Playback & streaming");
    expect(
      within(segment(playback, "Bitrate cap, remote")).getByRole("button", { name: "Default" }),
    ).toBeInTheDocument();
  });

  it("words an admin's defaults as the server default", async () => {
    const ui = userEvent.setup();
    mount({ ...USER, role: "admin", access_group_id: 5 });
    const playback = card("Playback & streaming");
    expect(
      within(playback).getByText("Every limit follows the server default."),
    ).toBeInTheDocument();
    expect(row(playback, "Simultaneous streams")).toBe("UnlimitedDEFAULT");
    await edit(ui, "Playback & streaming");
    expect(within(playback).getByText(/follows the server default\./)).toBeInTheDocument();
    expect(
      within(segment(playback, "Simultaneous streams")).getByRole("button", {
        name: "Default · Unlimited",
      }),
    ).toBeInTheDocument();
  });

  it("seeds a custom limit from the inherited value", async () => {
    const ui = userEvent.setup();
    mount(GUEST);
    const playback = await edit(ui, "Playback & streaming");
    await customize(ui, playback, "Simultaneous streams");
    expect(within(playback).getByRole("spinbutton", { name: "Simultaneous streams" })).toHaveValue(
      1,
    );
    await ui.click(within(playback).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ max_streams: 1 });
  });

  it("treats a cleared limit box as unsaved rather than as unlimited", async () => {
    const ui = userEvent.setup();
    mount();
    const playback = await edit(ui, "Playback & streaming");
    await customize(ui, playback, "Simultaneous streams");
    const streams = within(playback).getByRole("spinbutton", { name: "Simultaneous streams" });
    await ui.clear(streams);
    expect(
      within(playback).getByText("Enter a whole number, or switch back to Default."),
    ).toBeInTheDocument();
    expect(within(playback).getByRole("button", { name: "Save" })).toBeDisabled();
    await ui.type(streams, "3");
    await ui.click(within(playback).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ max_streams: 3 });
  });

  it("reverts an override to the default with null", async () => {
    const ui = userEvent.setup();
    mount({ ...USER, download_transcode_allowed: true });
    const downloads = await edit(ui, "Downloads");
    await ui.click(
      within(segment(downloads, "Server-prepared downloads")).getByRole("button", {
        name: "Default · Not allowed",
      }),
    );
    await ui.click(within(downloads).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ download_transcode_allowed: null });
  });

  it("offers an admin's full-access default from the server", async () => {
    const ui = userEvent.setup();
    mount({ ...USER, role: "admin", download_transcode_allowed: false });
    const downloads = await edit(ui, "Downloads");
    await ui.click(
      within(segment(downloads, "Server-prepared downloads")).getByRole("button", {
        name: "Default · Allowed",
      }),
    );
    await ui.click(within(downloads).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ download_transcode_allowed: null });
  });
});

describe("Playback & streaming", () => {
  it("overrides a bitrate cap in Mbps, seeded from the inherited cap", async () => {
    const ui = userEvent.setup();
    mount(GUEST);
    const playback = await edit(ui, "Playback & streaming");
    await customize(ui, playback, "Bitrate cap, remote");
    const remote = within(playback).getByRole("combobox", { name: "Bitrate cap, remote" });
    expect(remote).toHaveTextContent("8 Mbps");
    await ui.click(remote);
    await ui.click(await screen.findByRole("option", { name: "Custom" }));
    const custom = within(playback).getByLabelText("Bitrate cap, remote in Mbps");
    expect(custom).toHaveValue("8");
    await ui.clear(custom);
    await ui.type(custom, "2.5");
    await ui.click(within(playback).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ max_remote_stream_bitrate_kbps: 2500 });
  });

  it("starts a custom bitrate cap at 20 Mbps when the default is no cap", async () => {
    const ui = userEvent.setup();
    mount();
    const playback = await edit(ui, "Playback & streaming");
    await customize(ui, playback, "Bitrate cap, remote");
    expect(
      within(playback).getByRole("combobox", { name: "Bitrate cap, remote" }),
    ).toHaveTextContent("20 Mbps");
    await ui.click(within(playback).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ max_remote_stream_bitrate_kbps: 20000 });
  });

  it("sets and changes a max quality override", async () => {
    const ui = userEvent.setup();
    mount();
    let playback = await edit(ui, "Playback & streaming");
    await customize(ui, playback, "Max quality");
    await pick(ui, playback, "Max quality", "1080p");
    await ui.click(within(playback).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(1));
    expect(lastBody()).toEqual({ max_playback_quality: "1080p" });
    cleanup();

    // An account with an override opens straight into the Custom select.
    mount({ ...USER, max_playback_quality: "2160p" });
    playback = await edit(ui, "Playback & streaming");
    expect(within(playback).getByRole("combobox", { name: "Max quality" })).toHaveTextContent("4K");
    await pick(ui, playback, "Max quality", "Any");
    await ui.click(within(playback).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalledTimes(2));
    expect(lastBody()).toEqual({ max_playback_quality: "" });
  });

  it.each([
    ["Off", undefined, { transcode_allowed: false, max_transcodes: null }],
    ["Unlimited", undefined, { transcode_allowed: true, max_transcodes: 0 }],
    ["Up to", "2", { transcode_allowed: true, max_transcodes: 2 }],
  ])("maps video transcoding %s to the pair of fields", async (mode, max, body) => {
    const ui = userEvent.setup();
    mount({
      ...USER,
      transcode_allowed: true,
      max_transcodes: 1,
      effective_policy: { ...USER.effective_policy, max_transcodes: 1 },
    });
    const playback = await edit(ui, "Playback & streaming");
    await pick(ui, playback, "Video transcoding", mode);
    if (max) {
      const count = within(playback).getByRole("spinbutton", {
        name: "Video transcodes at a time",
      });
      await ui.clear(count);
      await ui.type(count, max);
    }
    await ui.click(within(playback).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual(body);
  });

  it("returns video transcoding to the default with both fields cleared", async () => {
    const ui = userEvent.setup();
    mount({ ...USER, transcode_allowed: false });
    const playback = await edit(ui, "Playback & streaming");
    await ui.click(
      within(segment(playback, "Video transcoding")).getByRole("button", {
        name: "Default · Unlimited",
      }),
    );
    await ui.click(within(playback).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ transcode_allowed: null, max_transcodes: null });
  });

  it("keeps an overridden count under an inherited switch when another row saves", async () => {
    const ui = userEvent.setup();
    mount({
      ...USER,
      max_transcodes: 3,
      effective_policy: { ...USER.effective_policy, max_transcodes: 3 },
    });
    const playback = card("Playback & streaming");
    expect(row(playback, "Video transcoding")).toContain("Up to 3 at a time");
    await edit(ui, "Playback & streaming");
    await customize(ui, playback, "Audio-only transcoding");
    await pick(ui, playback, "Audio-only transcoding", "Not allowed");
    await ui.click(within(playback).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(mocks.update).toHaveBeenCalled());
    expect(lastBody()).toEqual({ audio_transcode_allowed: false });
  });
});
