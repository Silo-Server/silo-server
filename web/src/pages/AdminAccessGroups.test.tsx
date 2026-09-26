import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { installPolicyStorageMocks, jsonResponse } from "./admin-policy/policyTestUtils";
import AdminAccessGroups from "./AdminAccessGroups";

vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({}) }));
const adminUsers = vi.hoisted(() => ({
  update: vi.fn(),
  currentGroups: new Map<number, number | null>(),
  data: [] as Array<{
    id: number;
    username: string;
    email: string;
    role: string;
    access_group_id: number | null;
  }>,
}));
vi.mock("@/hooks/queries/admin/users", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/queries/admin/users")>()),
  useAdminUsers: () => ({
    data: adminUsers.data,
    isPending: false,
    isError: false,
    isSuccess: true,
    refetch: vi.fn(),
  }),
}));
vi.mock("@/api/v2/adminUsers", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/adminUsers")>()),
  updateAdminUser: (...args: unknown[]) => adminUsers.update(...args),
  getAdminUser: async (id: number) => ({
    user: {
      id,
      access_group_id: adminUsers.currentGroups.has(id)
        ? adminUsers.currentGroups.get(id)
        : adminUsers.data.find((candidate) => candidate.id === id)?.access_group_id,
    },
    etag: `"user-${id}"`,
    profileContext: null,
  }),
}));
const toastSuccess = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { success: toastSuccess, error: vi.fn() } }));

// Radix Select needs these to open under jsdom.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
if (typeof globalThis.ResizeObserver === "undefined") {
  (globalThis as unknown as { ResizeObserver: typeof ResizeObserverStub }).ResizeObserver =
    ResizeObserverStub;
}
if (!window.HTMLElement.prototype.hasPointerCapture) {
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  window.HTMLElement.prototype.scrollIntoView = () => {};
}

async function pickOption(
  user: ReturnType<typeof userEvent.setup>,
  combobox: string,
  option: string,
) {
  await user.click(screen.getByRole("combobox", { name: combobox }));
  await user.click(await screen.findByRole("option", { name: option }));
}

const GROUP = {
  id: "1",
  name: "Kids",
  description: "",
  library_ids: ["2"],
  max_playback_quality: "1080p",
  download_allowed: false,
  download_transcode_allowed: false,
  transcode_allowed: true,
  audio_transcode_allowed: true,
  max_streams: 1,
  max_transcodes: 0,
  max_remote_stream_bitrate_kbps: 0,
  max_local_stream_bitrate_kbps: 0,
  allowed_permissions: [] as string[],
  requests_allowed: false,
  is_default: true,
  member_count: 3,
  created_at: "2026-07-02T12:00:00Z",
  updated_at: "2026-07-02T12:00:00Z",
};

function renderPage(initialPath = "/admin/access-groups") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter(
    [
      { path: "/admin", element: <p>Admin home</p> },
      { path: "/admin/access-groups", element: <AdminAccessGroups /> },
      { path: "/admin/access-groups/:id", element: <AdminAccessGroups /> },
    ],
    { initialEntries: ["/admin", initialPath], initialIndex: 1 },
  );
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return router;
}

describe("AdminAccessGroups", () => {
  let putBody: unknown;
  let group: typeof GROUP;

  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("account");
    setProfileId("owner");
    setProfileToken(null);
    putBody = undefined;
    group = GROUP;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (url === "/api/v2/admin/users/capabilities")
          return jsonResponse({ access_groups: true });
        if (url === "/api/v2/admin/access-groups?limit=200" && method === "GET") {
          return jsonResponse({ items: [group], page: { has_more: false } });
        }
        if (url === "/api/v2/admin/access-groups/1" && method === "GET") {
          return new Response(JSON.stringify(group), {
            headers: { "Content-Type": "application/json", ETag: '"initial"' },
          });
        }
        if (url === "/api/v1/admin/libraries") {
          return jsonResponse([
            { id: 2, name: "Movies", type: "movie", enabled: true },
            { id: 3, name: "Anime", type: "series", enabled: true },
          ]);
        }
        if (url === "/api/v2/libraries") {
          return jsonResponse({
            items: [
              { id: "2", name: "Movies", type: "movie", enabled: true },
              { id: "3", name: "Anime", type: "series", enabled: true },
            ],
          });
        }
        if (url === "/api/v2/admin/access-groups/1" && method === "PUT") {
          putBody = JSON.parse(String(init?.body));
          return new Response(JSON.stringify({ ...GROUP, download_allowed: true }), {
            headers: { "Content-Type": "application/json", ETag: '"saved"' },
          });
        }
        return jsonResponse({ error: "not_found", message: url }, 404);
      }),
    );
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    adminUsers.currentGroups.clear();
  });

  it("summarizes a group and saves edited restrictions", async () => {
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("Kids")).toBeInTheDocument();
    expect(screen.getByText("3 members")).toBeInTheDocument();
    // Card facts reflect the restriction shape; default groups are labeled.
    expect(screen.getByText("No downloads")).toBeInTheDocument();
    expect(screen.getByText("Default")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Kids/ }));

    // Drill-in editor seeds from the group; toggle downloads on and save.
    fireEvent.click(await screen.findByRole("switch", { name: "Allow downloads" }));
    await pickOption(user, "Max remote stream bitrate", "8 Mbps");
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => {
      expect(putBody).toMatchObject({
        name: "Kids",
        library_ids: ["2"],
        download_allowed: true,
        max_streams: 1,
        max_remote_stream_bitrate_kbps: 8000,
        max_local_stream_bitrate_kbps: 0,
        requests_allowed: false,
        allowed_permissions: [],
        is_default: true,
      });
    });
  });

  it("lists the group's members with links to their user pages", async () => {
    const user = (id: number, username: string, role: string, group: number | null) => ({
      id,
      username,
      email: `${username}@example.test`,
      role,
      access_group_id: group,
    });
    adminUsers.data = [
      user(7, "taylor", "user", 1),
      user(8, "sam", "user", 2),
      user(9, "robin", "user", null),
      user(10, "root", "admin", null),
    ];
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });
    expect(within(members).getByRole("link", { name: "taylor" })).toHaveAttribute(
      "href",
      "/admin/users/7",
    );
    expect(within(members).queryByRole("link", { name: "sam" })).toBeNull();
    expect(within(members).queryByRole("link", { name: "robin" })).toBeNull();
    expect(within(members).queryByRole("link", { name: "root" })).toBeNull();
    adminUsers.data = [];
  });

  function withGuestsGroup() {
    const serve = globalThis.fetch;
    const guests = {
      ...GROUP,
      id: "2",
      name: "Guests",
      library_ids: ["3"],
      is_default: false,
      download_allowed: true,
    };
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) =>
        String(input) === "/api/v2/admin/access-groups?limit=200" &&
        (init?.method ?? "GET") === "GET"
          ? jsonResponse({ items: [GROUP, guests], page: { has_more: false } })
          : serve(input, init),
      ),
    );
  }

  const member = (id: number, username: string, role: string, group: number | null) => ({
    id,
    username,
    email: `${username}@example.test`,
    role,
    access_group_id: group,
  });

  it("moves selected members to another group after confirming the policy changes", async () => {
    withGuestsGroup();
    adminUsers.data = [member(7, "taylor", "user", 1), member(8, "sam", "user", 1)];
    adminUsers.update.mockReset().mockResolvedValue(undefined);
    toastSuccess.mockClear();
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });

    await user.click(within(members).getByRole("checkbox", { name: "Select taylor" }));
    await pickOption(user, "Move selected members to", "Guests");
    await user.click(within(members).getByRole("button", { name: /Move 1 selected/ }));

    const confirm = await screen.findByRole("alertdialog");
    expect(within(confirm).getByText("Move 1 user to Guests?")).toBeInTheDocument();
    expect(within(confirm).getByText("1 from Kids")).toBeInTheDocument();
    expect(within(confirm).getByText("Libraries: Movies → Anime")).toBeInTheDocument();
    expect(within(confirm).getByText("Downloads: Not allowed → Allowed")).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Move" }));

    await waitFor(() => expect(adminUsers.update).toHaveBeenCalledTimes(1));
    const call = adminUsers.update.mock.calls[0]!;
    expect(call[0].user.id).toBe(7);
    expect(String(call[1].access_group_id)).toBe("2");
    expect(toastSuccess).toHaveBeenCalledWith("Moved 1 user to Guests");
    adminUsers.data = [];
  });

  it("adds eligible users to the group, showing where they come from", async () => {
    withGuestsGroup();
    adminUsers.data = [
      member(7, "taylor", "user", 1),
      member(8, "sam", "user", 2),
      member(9, "robin", "user", null),
      member(10, "root", "admin", null),
    ];
    adminUsers.update.mockReset().mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });

    await user.click(within(members).getByRole("button", { name: "Add users" }));
    const dialog = await screen.findByRole("dialog");
    // Members and admin accounts aren't offered.
    expect(within(dialog).queryByText("taylor")).toBeNull();
    expect(within(dialog).queryByText("root")).toBeNull();
    expect(within(dialog).getByText("sam").closest("label")).toHaveTextContent("Guests");
    expect(within(dialog).getByText("robin").closest("label")).toHaveTextContent("No group");

    await user.type(within(dialog).getByLabelText("Search users"), "rob");
    expect(within(dialog).queryByText("sam")).toBeNull();
    await user.click(within(dialog).getByRole("checkbox"));
    await user.click(within(dialog).getByRole("button", { name: /Add 1 selected/ }));

    const confirm = await screen.findByRole("alertdialog");
    expect(within(confirm).getByText("Move 1 user to Kids?")).toBeInTheDocument();
    expect(within(confirm).getByText("1 from no group")).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Move" }));

    await waitFor(() => expect(adminUsers.update).toHaveBeenCalledTimes(1));
    expect(adminUsers.update.mock.calls[0]![0].user.id).toBe(9);
    expect(String(adminUsers.update.mock.calls[0]![1].access_group_id)).toBe("1");
    adminUsers.data = [];
  });

  it("names members that could not be moved", async () => {
    withGuestsGroup();
    adminUsers.data = [member(7, "taylor", "user", 1), member(8, "sam", "user", 1)];
    adminUsers.update
      .mockReset()
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error("This user changed."));
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });

    await user.click(within(members).getByRole("checkbox", { name: "Select sam" }));
    await user.click(within(members).getByRole("checkbox", { name: "Select taylor" }));
    await pickOption(user, "Move selected members to", "Guests");
    await user.click(within(members).getByRole("button", { name: /Move 2 selected/ }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Move" }),
    );

    const alert = await within(members).findByRole("alert");
    expect(alert).toHaveTextContent("Some users could not be moved");
    expect(alert).toHaveTextContent("taylor: This user changed.");
    // The failed member stays selected with the same target, ready to retry.
    expect(within(members).getByRole("checkbox", { name: "Select taylor" })).toBeChecked();
    expect(within(members).getByRole("checkbox", { name: "Select sam" })).not.toBeChecked();
    expect(
      within(members).getByRole("combobox", { name: "Move selected members to" }),
    ).toHaveTextContent("Guests");
    adminUsers.update.mockResolvedValue(undefined);
    await user.click(within(members).getByRole("button", { name: /Move 1 selected/ }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Move" }),
    );
    await waitFor(() => expect(adminUsers.update).toHaveBeenCalledTimes(3));
    expect(adminUsers.update.mock.calls[2]![0].user.id).toBe(7);
    adminUsers.data = [];
  });

  it("does not move a member whose group changed after confirmation", async () => {
    withGuestsGroup();
    adminUsers.data = [member(7, "taylor", "user", 1)];
    adminUsers.update.mockReset().mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });

    await user.click(within(members).getByRole("checkbox", { name: "Select taylor" }));
    await pickOption(user, "Move selected members to", "Guests");
    await user.click(within(members).getByRole("button", { name: /Move 1 selected/ }));
    adminUsers.currentGroups.set(7, 2);
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Move" }),
    );

    expect(await within(members).findByRole("alert")).toHaveTextContent(
      "taylor: This user's group changed. Reload and try again.",
    );
    expect(adminUsers.update).not.toHaveBeenCalled();
    adminUsers.data = [];
  });

  it("refreshes user and group lists once after moving several members", async () => {
    withGuestsGroup();
    adminUsers.data = [member(7, "taylor", "user", 1), member(8, "sam", "user", 1)];
    adminUsers.update.mockReset().mockResolvedValue(undefined);
    const invalidations = vi.spyOn(QueryClient.prototype, "invalidateQueries");
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });

    await user.click(within(members).getByRole("checkbox", { name: "Select taylor" }));
    await user.click(within(members).getByRole("checkbox", { name: "Select sam" }));
    await pickOption(user, "Move selected members to", "Guests");
    await user.click(within(members).getByRole("button", { name: /Move 2 selected/ }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Move" }),
    );

    await waitFor(() => expect(adminUsers.update).toHaveBeenCalledTimes(2));
    expect(invalidations).toHaveBeenCalledTimes(2);
    adminUsers.data = [];
  });

  it("returns to the group list with a confirmation after saving", async () => {
    toastSuccess.mockClear();
    const router = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    fireEvent.click(await screen.findByRole("switch", { name: "Allow downloads" }));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(await screen.findByRole("heading", { name: "Access Groups" })).toBeInTheDocument();
    expect(putBody).toMatchObject({ download_allowed: true });
    expect(toastSuccess).toHaveBeenCalledWith("Group saved");
    expect(router.state.location.pathname).toBe("/admin/access-groups");
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();

    // The save replaced the group's entry, so Back doesn't reopen the editor.
    await router.navigate(-1);
    expect(router.state.location.pathname).toBe("/admin/access-groups");
  });

  it("stays in the editor with the error when a save fails", async () => {
    toastSuccess.mockClear();
    const serve = globalThis.fetch;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) =>
        String(input) === "/api/v2/admin/access-groups/1" && init?.method === "PUT"
          ? jsonResponse({ error: "internal_error", message: "Could not save the group." }, 500)
          : serve(input, init),
      ),
    );
    const router = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    fireEvent.click(await screen.findByRole("switch", { name: "Allow downloads" }));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/access-groups/1");
    expect(screen.getByRole("button", { name: "Save changes" })).toBeInTheDocument();
    expect(toastSuccess).not.toHaveBeenCalled();
  });

  it("opens a group at its own URL so Back returns to the group list", async () => {
    const router = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    expect(await screen.findByRole("button", { name: "All groups" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/access-groups/1");

    await router.navigate(-1);
    expect(await screen.findByRole("heading", { name: "Access Groups" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/access-groups");
    expect(screen.queryByRole("button", { name: "All groups" })).not.toBeInTheDocument();
  });

  it("retries a failed group load when the same group is opened again", async () => {
    const serve = globalThis.fetch;
    let groupReads = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        if (
          String(input) === "/api/v2/admin/access-groups/1" &&
          (init?.method ?? "GET") === "GET"
        ) {
          groupReads += 1;
          if (groupReads === 1) return jsonResponse({ error: "unavailable", message: "down" }, 503);
        }
        return serve(input, init);
      }),
    );
    const router = renderPage("/admin/access-groups/1");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByText("Loading group editor...")).not.toBeInTheDocument();

    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    expect(await screen.findByLabelText("Name")).toHaveValue("Kids");
    expect(groupReads).toBe(2);
    expect(router.state.location.pathname).toBe("/admin/access-groups/1");
  });

  it("clears the loading message when leaving a group before it loads", async () => {
    const serve = globalThis.fetch;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) =>
        String(input) === "/api/v2/admin/access-groups/1" && (init?.method ?? "GET") === "GET"
          ? new Promise<Response>(() => {})
          : serve(input, init),
      ),
    );
    const router = renderPage("/admin/access-groups/1");
    expect(await screen.findByText("Loading group editor...")).toBeInTheDocument();
    await router.navigate("/admin/access-groups");
    await waitFor(() =>
      expect(screen.queryByText("Loading group editor...")).not.toBeInTheDocument(),
    );
  });

  function holdCreate() {
    const serve = globalThis.fetch;
    let finish: () => void = () => {};
    const created = new Promise<void>((resolve) => {
      finish = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        if (String(input) === "/api/v2/admin/access-groups" && init?.method === "POST") {
          await created;
          return jsonResponse({ ...GROUP, id: "7", name: "Guests", is_default: false }, 201);
        }
        return serve(input, init);
      }),
    );
    return finish;
  }

  async function startCreate(name: string) {
    fireEvent.click(await screen.findByRole("button", { name: /New group/ }));
    fireEvent.change(screen.getByLabelText("New group name"), { target: { value: name } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));
  }

  it("opens a newly created group", async () => {
    const finish = holdCreate();
    const router = renderPage();
    await startCreate("Guests");
    finish();
    await waitFor(() => expect(router.state.location.pathname).toBe("/admin/access-groups/7"));
  });

  it("keeps the admin on a group they opened while another was being created", async () => {
    const finish = holdCreate();
    const router = renderPage();
    await startCreate("Guests");
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    expect(await screen.findByLabelText("Name")).toHaveValue("Kids");

    finish();
    await waitFor(() => expect(screen.queryByRole("button", { name: "Create" })).toBeNull());
    expect(router.state.location.pathname).toBe("/admin/access-groups/1");
    expect(screen.getByLabelText("Name")).toHaveValue("Kids");
  });

  it("keeps the admin on a group they reopened while it was being deleted", async () => {
    const serve = globalThis.fetch;
    let finish: () => void = () => {};
    const deleted = new Promise<void>((resolve) => {
      finish = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        const method = init?.method ?? "GET";
        if (String(input) === "/api/v2/admin/access-groups/1" && method === "GET") {
          return new Response(JSON.stringify({ ...GROUP, is_default: false }), {
            headers: { "Content-Type": "application/json", ETag: '"initial"' },
          });
        }
        if (method === "DELETE") {
          await deleted;
          return new Response(null, { status: 204 });
        }
        return serve(input, init);
      }),
    );
    const router = renderPage("/admin/access-groups/1");
    fireEvent.click(await screen.findByRole("button", { name: "Delete group" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    await router.navigate("/admin/access-groups");
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    expect(await screen.findByLabelText("Name")).toHaveValue("Kids");
    const reopened = router.state.location.key;

    finish();
    await deleted;
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(router.state.location.key).toBe(reopened);
    expect(router.state.location.pathname).toBe("/admin/access-groups/1");
  });

  it("opens the group editor when loaded from a group URL", async () => {
    const router = renderPage("/admin/access-groups/1");
    expect(await screen.findByLabelText("Name")).toHaveValue("Kids");
    fireEvent.click(screen.getByRole("button", { name: "All groups" }));
    expect(await screen.findByRole("heading", { name: "Access Groups" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/access-groups");
  });

  it("locks demotion and deletion for the default group", async () => {
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));

    // The server rejects demoting or deleting the default group, so the
    // editor disables both paths and explains the promote-another-group flow.
    expect(await screen.findByRole("switch", { name: "Default for new users" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /delete group/i })).toBeDisabled();
    expect(screen.getByText(/make another group the default first/i)).toBeInTheDocument();
  });

  it("saves a custom Mbps bitrate limit as whole kbps", async () => {
    const user = userEvent.setup();
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    await screen.findByRole("combobox", { name: "Max remote stream bitrate" });
    await pickOption(user, "Max remote stream bitrate", "Custom");
    await user.type(screen.getByLabelText("Max remote stream bitrate in Mbps"), "1.5");
    await pickOption(user, "Max local stream bitrate", "40 Mbps");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => {
      expect(putBody).toMatchObject({
        max_remote_stream_bitrate_kbps: 1500,
        max_local_stream_bitrate_kbps: 40000,
      });
    });
  });

  it("opens a non-preset limit as a custom Mbps value", async () => {
    group = { ...GROUP, max_remote_stream_bitrate_kbps: 4500 };
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    expect(await screen.findByLabelText("Max remote stream bitrate in Mbps")).toHaveValue("4.5");
    expect(screen.getByRole("combobox", { name: "Max remote stream bitrate" })).toHaveTextContent(
      "Custom",
    );
    expect(screen.getByRole("combobox", { name: "Max local stream bitrate" })).toHaveTextContent(
      "Unlimited",
    );
  });

  it("blocks saving until a custom limit is a valid Mbps value and warns below 1 Mbps", async () => {
    const user = userEvent.setup();
    group = { ...GROUP, max_remote_stream_bitrate_kbps: 4000 };
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    const limit = await screen.findByLabelText("Max remote stream bitrate in Mbps");
    const save = screen.getByRole("button", { name: "Save changes" });

    // A cleared box is an unsaved edit, never a silent 0 (unlimited).
    await user.clear(limit);
    expect(save).toBeDisabled();
    expect(screen.getByText(/Enter a value above 0 Mbps/)).toBeInTheDocument();

    // 0 means unlimited and has its own choice, so a half-typed "0." is not a cap.
    await user.type(limit, "0.");
    expect(save).toBeDisabled();
    // kbps resolution is the floor; finer values are rejected, not rounded.
    await user.type(limit, "0005");
    expect(limit).toHaveValue("0.0005");
    expect(save).toBeDisabled();

    await user.clear(limit);
    await user.type(limit, "0.5");
    expect(save).toBeEnabled();
    expect(screen.getByText(/Below 1 Mbps/)).toBeInTheDocument();
    fireEvent.click(save);
    await waitFor(() => {
      expect(putBody).toMatchObject({ max_remote_stream_bitrate_kbps: 500 });
    });
  });
});

it("keeps a stale draft and requires explicit canonical reload before resubmission", async () => {
  installPolicyStorageMocks();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  let reads = 0;
  const tags: (string | null)[] = [];
  const bodies: Record<string, unknown>[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url === "/api/v2/admin/users/capabilities") return jsonResponse({ access_groups: true });
      if (url.includes("/admin/access-groups?"))
        return jsonResponse({ items: [GROUP], page: { has_more: false } });
      if (url === "/api/v2/admin/access-groups/1" && method === "GET") {
        reads++;
        return new Response(
          JSON.stringify({ ...GROUP, name: reads === 1 ? "Canonical" : "Someone else's name" }),
          {
            headers: {
              "Content-Type": "application/json",
              ETag: reads === 1 ? '"old"' : '"fresh"',
            },
          },
        );
      }
      if (method === "PUT") {
        tags.push(new Headers(init?.headers).get("If-Match"));
        bodies.push(JSON.parse(String(init?.body)));
        if (tags.length === 1)
          return new Response(
            JSON.stringify({
              type: "https://silo.example/problems/precondition_failed",
              title: "Changed",
              status: 412,
              detail: "Reload current state",
            }),
            {
              status: 412,
              headers: { "Content-Type": "application/problem+json", ETag: '"must-not-adopt"' },
            },
          );
        return new Response(JSON.stringify(GROUP), {
          headers: { "Content-Type": "application/json", ETag: '"saved"' },
        });
      }
      if (url.includes("libraries")) return jsonResponse([]);
      return jsonResponse({}, 404);
    }),
  );
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
  const name = await screen.findByLabelText("Name");
  expect(name).toHaveValue("Canonical");
  fireEvent.change(name, { target: { value: "My draft" } });
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await screen.findByText(/Your draft is preserved/);
  expect(name).toHaveValue("My draft");
  expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  expect(reads).toBe(1);
  fireEvent.click(screen.getByRole("button", { name: "Reload current group" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Save changes" })).toBeEnabled());
  expect(name).toHaveValue("My draft");
  fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
  await waitFor(() => expect(tags).toEqual(['"old"', '"fresh"']));
  expect(bodies.map((body) => body.name)).toEqual(["My draft", "My draft"]);
  cleanup();
  vi.unstubAllGlobals();
});

it("keeps delete confirmation after conflict and reloads before retry", async () => {
  installPolicyStorageMocks();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  let reads = 0;
  const tags: (string | null)[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? "GET";
      if (url === "/api/v2/admin/users/capabilities") return jsonResponse({ access_groups: true });
      if (url.includes("/admin/access-groups?"))
        return jsonResponse({ items: [GROUP], page: { has_more: false } });
      if (url === "/api/v2/admin/access-groups/1" && method === "GET") {
        reads++;
        return new Response(JSON.stringify({ ...GROUP, is_default: false }), {
          headers: { "Content-Type": "application/json", ETag: reads === 1 ? '"old"' : '"fresh"' },
        });
      }
      if (method === "DELETE") {
        tags.push(new Headers(init?.headers).get("If-Match"));
        if (tags.length === 1)
          return new Response(
            JSON.stringify({
              type: "https://silo.example/problems/precondition_failed",
              title: "Changed",
              status: 412,
              detail: "Reload current state",
            }),
            { status: 412, headers: { "Content-Type": "application/problem+json" } },
          );
        return new Response(null, { status: 204 });
      }
      return jsonResponse([]);
    }),
  );
  const router = renderPage();
  fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
  fireEvent.click(await screen.findByRole("button", { name: "Delete group" }));
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled());
  expect(screen.getByRole("alertdialog")).toBeInTheDocument();
  expect(reads).toBe(1);
  fireEvent.click(screen.getByRole("button", { name: "Reload current group" }));
  await waitFor(() => expect(screen.getByRole("button", { name: "Delete" })).toBeEnabled());
  fireEvent.click(screen.getByRole("button", { name: "Delete" }));
  await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  expect(tags).toEqual(['"old"', '"fresh"']);
  // Deleting replaces the group's history entry, so Back can't reopen it.
  await waitFor(() => expect(router.state.location.pathname).toBe("/admin/access-groups"));
  await router.navigate(-1);
  expect(router.state.location.pathname).toBe("/admin/access-groups");
  cleanup();
  vi.unstubAllGlobals();
});

it("blocks configuration controls when capability is unavailable", async () => {
  installPolicyStorageMocks();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) =>
    String(input).includes("capabilities")
      ? jsonResponse({ access_groups: false })
      : jsonResponse({ items: [GROUP], page: { has_more: false } }),
  );
  vi.stubGlobal("fetch", fetch);
  renderPage();
  fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
  expect(screen.getByText("Access group editing is unavailable.")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: "New group" })).not.toBeInTheDocument();
  expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
  expect(fetch.mock.calls.every(([url]) => !String(url).endsWith("/1"))).toBe(true);
  cleanup();
  vi.unstubAllGlobals();
});
