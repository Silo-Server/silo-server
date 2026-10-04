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
  data: [] as Array<{
    id: number;
    username: string;
    email: string;
    role: string;
    access_group_id: number | null;
  }>,
}));
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({
    data: adminUsers.data,
    isPending: false,
    isError: false,
    isSuccess: true,
    refetch: vi.fn(),
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

const REQUEST_SETTINGS = {
  requests_enabled: true,
  global_max_requests: 12,
  global_window_days: 14,
  global_auto_approval_enabled: true,
  force_dual_quality: false,
};
const INHERIT_LIMIT = {
  group_id: "1",
  limit_mode: "inherit",
  max_requests: null,
  window_days: null,
  approval_mode: "inherit",
};

describe("AdminAccessGroups", () => {
  let putBody: unknown;
  let group: typeof GROUP;
  // The group's request limit as stored, and every write to it, in order.
  let groupLimit: Record<string, unknown>;
  let groupLimitTag: string;
  let writes: Array<{ url: string; ifMatch: string | null; body: Record<string, unknown> }>;
  let refuseLimitWrites: number;

  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("account");
    setProfileId("owner");
    setProfileToken(null);
    putBody = undefined;
    group = GROUP;
    groupLimit = INHERIT_LIMIT;
    groupLimitTag = '"limit-initial"';
    writes = [];
    refuseLimitWrites = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (method === "PUT") {
          writes.push({
            url,
            ifMatch: new Headers(init?.headers).get("If-Match"),
            body: JSON.parse(String(init?.body)),
          });
        }
        if (url === "/api/v2/admin/request-settings") return jsonResponse(REQUEST_SETTINGS);
        if (url === "/api/v2/admin/request-groups/1/limit" && method === "GET") {
          return jsonResponse(groupLimit, 200, groupLimitTag);
        }
        if (url === "/api/v2/admin/request-groups/1/limit" && method === "PUT") {
          if (refuseLimitWrites > 0) {
            refuseLimitWrites--;
            groupLimitTag = '"limit-newer"';
            return jsonResponse(
              {
                type: "https://silo.example/problems/precondition_failed",
                title: "Changed",
                status: 412,
                detail: "The access group's request limit changed; reload before saving.",
              },
              412,
              groupLimitTag,
            );
          }
          groupLimit = { group_id: "1", ...JSON.parse(String(init?.body)) };
          groupLimitTag = '"limit-saved"';
          return jsonResponse(groupLimit, 200, groupLimitTag);
        }
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
  });

  it("summarizes a group and saves edited restrictions", async () => {
    toastSuccess.mockClear();
    const user = userEvent.setup();
    const router = renderPage();

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
    // The request limit was not edited, so nothing wrote it.
    expect(writes.map((write) => write.url)).toEqual(["/api/v2/admin/access-groups/1"]);
    expect(await screen.findByRole("heading", { name: "Access Groups" })).toBeInTheDocument();
    expect(toastSuccess).toHaveBeenCalledWith("Group saved");
    expect(router.state.location.pathname).toBe("/admin/access-groups");
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
    await router.navigate(-1);
    expect(router.state.location.pathname).toBe("/admin/access-groups");
  });

  it("saves only the request approval and limit when no group field changed", async () => {
    group = { ...GROUP, requests_allowed: true };
    toastSuccess.mockClear();
    const user = userEvent.setup();
    const router = renderPage("/admin/access-groups/1");
    const requests = await screen.findByRole("region", { name: "Requests" });
    // Inheriting fields say what the server-wide default is.
    expect(
      await within(requests).findByText("Server default: approved automatically"),
    ).toBeInTheDocument();
    expect(
      within(requests).getByText("Server default: 12 requests per 14 days"),
    ).toBeInTheDocument();

    await pickOption(user, "Approval", "An admin approves");
    await user.click(screen.getByRole("combobox", { name: "Limit" }));
    expect((await screen.findAllByRole("option")).map((option) => option.textContent)).toEqual([
      "Use server default",
      "Custom limit",
      "No limit",
    ]);
    await user.click(screen.getByRole("option", { name: "Custom limit" }));
    const max = screen.getByRole("spinbutton", { name: "Requests allowed" });
    expect(max).toHaveValue(12);
    await user.clear(max);
    await user.type(max, "3");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(await screen.findByRole("heading", { name: "Access Groups" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/access-groups");
    expect(toastSuccess).toHaveBeenCalledWith("Group saved");
    // The group itself was untouched, so its revision is left alone.
    expect(writes.map((write) => write.url)).toEqual(["/api/v2/admin/request-groups/1/limit"]);
    expect(writes[0]).toMatchObject({
      ifMatch: '"limit-initial"',
      body: { limit_mode: "custom", max_requests: 3, window_days: 14, approval_mode: "manual" },
    });
    expect(await screen.findByText("Admin approves · 3 per 14 days")).toBeInTheDocument();
    expect(screen.getByText("Requests on")).toBeInTheDocument();
  });

  it("saves a group's limit of zero and says it only stops new requests", async () => {
    groupLimit = { ...INHERIT_LIMIT, limit_mode: "custom", max_requests: 0, window_days: 7 };
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const max = await screen.findByRole("spinbutton", { name: "Requests allowed" });
    expect(max).toHaveValue(0);
    expect(
      screen.getByText(
        "0 stops new requests; to block this group, turn off Media requests instead.",
      ),
    ).toBeInTheDocument();
    await pickOption(user, "Approval", "Approve automatically");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("heading", { name: "Access Groups" })).toBeInTheDocument();
    expect(writes).toEqual([
      expect.objectContaining({
        url: "/api/v2/admin/request-groups/1/limit",
        body: { limit_mode: "custom", max_requests: 0, window_days: 7, approval_mode: "auto" },
      }),
    ]);
  });

  it("stays on the group when its request limit could not be saved, and retries only the limit", async () => {
    toastSuccess.mockClear();
    refuseLimitWrites = 1;
    const user = userEvent.setup();
    const router = renderPage("/admin/access-groups/1");
    await screen.findByRole("region", { name: "Requests" });
    fireEvent.click(screen.getByRole("switch", { name: "Allow downloads" }));
    await pickOption(user, "Limit", "No limit");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(
      await screen.findByText(/The group was saved, but its request approval and limit were not/),
    ).toBeInTheDocument();
    expect(screen.getByText(/changed by another administrator/)).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/access-groups/1");
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();

    await user.click(screen.getByRole("button", { name: "Reload latest version" }));
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Limit" })).toHaveTextContent(
        "Use server default",
      ),
    );
    await pickOption(user, "Limit", "No limit");
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("heading", { name: "Access Groups" })).toBeInTheDocument();
    // The group was written once; the retry wrote only the limit.
    expect(writes.map((write) => write.url)).toEqual([
      "/api/v2/admin/access-groups/1",
      "/api/v2/admin/request-groups/1/limit",
      "/api/v2/admin/request-groups/1/limit",
    ]);
    const limitWrites = writes.filter((write) => write.url.includes("request-groups"));
    expect(limitWrites.map((write) => write.ifMatch)).toEqual(['"limit-initial"', '"limit-newer"']);
    expect(limitWrites[1]?.body).toMatchObject({ limit_mode: "unlimited", max_requests: null });
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

  it("locks demotion and deletion for the default group", async () => {
    const router = renderPage("/admin/access-groups/1");
    expect(await screen.findByLabelText("Name")).toHaveValue("Kids");

    // The server rejects demoting or deleting the default group, so the
    // editor disables both paths and explains the promote-another-group flow.
    expect(await screen.findByRole("switch", { name: "Default for new users" })).toBeDisabled();
    expect(screen.getByRole("button", { name: /delete group/i })).toBeDisabled();
    expect(screen.getByText(/make another group the default first/i)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "All groups" }));
    expect(await screen.findByRole("heading", { name: "Access Groups" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/admin/access-groups");
  });

  it("says a deleted group's members move to the default group", async () => {
    group = { ...GROUP, is_default: false };
    const everyone = { ...GROUP, id: "2", name: "Everyone", is_default: true, member_count: 0 };
    const serve = globalThis.fetch;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) =>
        String(input) === "/api/v2/admin/access-groups?limit=200" &&
        (init?.method ?? "GET") === "GET"
          ? jsonResponse({ items: [group, everyone], page: { has_more: false } })
          : serve(input, init),
      ),
    );
    renderPage("/admin/access-groups/1");

    fireEvent.click(await screen.findByRole("button", { name: "Delete group" }));
    const dialog = await screen.findByRole("alertdialog");
    await waitFor(() =>
      expect(dialog).toHaveTextContent(
        "3 members will move to the default group, Everyone, and get its access right away, without being signed out",
      ),
    );
    expect(dialog).not.toHaveTextContent("no group");
  });

  it("keeps the move warning when the cached member count is zero", async () => {
    // The list's count can be stale: an account may have joined since it loaded.
    group = { ...GROUP, is_default: false, member_count: 0 };
    const everyone = { ...GROUP, id: "2", name: "Everyone", is_default: true, member_count: 0 };
    const serve = globalThis.fetch;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) =>
        String(input) === "/api/v2/admin/access-groups?limit=200" &&
        (init?.method ?? "GET") === "GET"
          ? jsonResponse({ items: [group, everyone], page: { has_more: false } })
          : serve(input, init),
      ),
    );
    renderPage("/admin/access-groups/1");

    fireEvent.click(await screen.findByRole("button", { name: "Delete group" }));
    const dialog = await screen.findByRole("alertdialog");
    await waitFor(() =>
      expect(dialog).toHaveTextContent(
        "Any members (none when this list last loaded) will move to the default group, Everyone, and get its access right away, without being signed out",
      ),
    );
    expect(dialog).not.toHaveTextContent("This group has no members");
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

  it("blocks saving until a custom limit is a valid Mbps value and warns below 1 Mbps", async () => {
    const user = userEvent.setup();
    group = { ...GROUP, max_remote_stream_bitrate_kbps: 4500 };
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /Kids/ }));
    const limit = await screen.findByLabelText("Max remote stream bitrate in Mbps");
    expect(limit).toHaveValue("4.5");
    expect(screen.getByRole("combobox", { name: "Max remote stream bitrate" })).toHaveTextContent(
      "Custom",
    );
    expect(screen.getByRole("combobox", { name: "Max local stream bitrate" })).toHaveTextContent(
      "Unlimited",
    );
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
