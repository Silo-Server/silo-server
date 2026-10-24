// @vitest-environment jsdom
// Moving and adding an access group's members, from the group editor.
import { QueryClient } from "@tanstack/react-query";
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "./admin-policy/policyTestUtils";
import { stubRadixDom } from "./adminPage.test-support";
import {
  GROUP,
  INHERIT_LIMIT,
  pickOption,
  renderPage,
  REQUEST_SETTINGS,
} from "./AdminAccessGroups.test-support";

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
    requests_allowed?: boolean | null;
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

stubRadixDom();

describe("AdminAccessGroups members", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setAccessToken("account");
    setProfileId("owner");
    setProfileToken(null);
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) => {
        const url = String(input);
        const method = init?.method ?? "GET";
        if (url === "/api/v2/admin/request-settings") return jsonResponse(REQUEST_SETTINGS);
        if (url === "/api/v2/admin/request-groups/1/limit" && method === "GET") {
          return jsonResponse(INHERIT_LIMIT, 200, '"limit-initial"');
        }
        if (url === "/api/v2/admin/users/capabilities")
          return jsonResponse({ access_groups: true });
        if (url === "/api/v2/admin/access-groups?limit=200" && method === "GET") {
          return jsonResponse({ items: [GROUP], page: { has_more: false } });
        }
        if (url === "/api/v2/admin/access-groups/1" && method === "GET") {
          return new Response(JSON.stringify(GROUP), {
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

  it.each([
    { groupRequestsAllowed: true, userRequestsAllowed: null },
    { groupRequestsAllowed: false, userRequestsAllowed: true },
  ])(
    "shows request term changes with access $groupRequestsAllowed and override $userRequestsAllowed",
    async ({ groupRequestsAllowed, userRequestsAllowed }) => {
      // Requests are enabled by the group or an account override. The groups
      // otherwise match, but their separately stored request terms differ.
      const serve = globalThis.fetch;
      const kids = { ...GROUP, requests_allowed: groupRequestsAllowed };
      const guests = { ...kids, id: "2", name: "Guests", is_default: false };
      vi.stubGlobal(
        "fetch",
        vi.fn<typeof fetch>(async (input, init) => {
          const url = String(input);
          if (url === "/api/v2/admin/access-groups?limit=200") {
            return jsonResponse({ items: [kids, guests], page: { has_more: false } });
          }
          if (url === "/api/v2/admin/access-groups/1") {
            return new Response(JSON.stringify(kids), {
              headers: { "Content-Type": "application/json", ETag: '"initial"' },
            });
          }
          if (url === "/api/v2/admin/request-groups/1/limit") {
            return jsonResponse(
              {
                group_id: "1",
                limit_mode: "custom",
                max_requests: 0,
                window_days: 7,
                approval_mode: "manual",
              },
              200,
              '"kids"',
            );
          }
          if (url === "/api/v2/admin/request-groups/2/limit") {
            return jsonResponse(
              {
                group_id: "2",
                limit_mode: "unlimited",
                max_requests: null,
                window_days: null,
                approval_mode: "auto",
              },
              200,
              '"guests"',
            );
          }
          return serve(input, init);
        }),
      );
      adminUsers.data = [
        { ...member(7, "taylor", "user", 1), requests_allowed: userRequestsAllowed },
      ];
      adminUsers.update.mockReset().mockResolvedValue(undefined);
      const user = userEvent.setup();
      renderPage("/admin/access-groups/1");
      const members = await screen.findByRole("region", { name: "Members" });

      await user.click(within(members).getByRole("checkbox", { name: "Select taylor" }));
      await pickOption(user, "Move selected members to", "Guests");
      await user.click(within(members).getByRole("button", { name: /Move 1 selected/ }));

      const confirm = await screen.findByRole("alertdialog");
      expect(
        await within(confirm).findByText(
          "Request limit: no new requests (0 per 7 days) → no limit",
        ),
      ).toBeInTheDocument();
      expect(
        within(confirm).getByText("Request approval: an admin approves → approved automatically"),
      ).toBeInTheDocument();
      expect(within(confirm).queryByText("No inherited settings change.")).not.toBeInTheDocument();
      adminUsers.data = [];
    },
  );

  it("confirms adding users against the group list's current policy", async () => {
    // The editor's own snapshot of Kids predates the shared group list, which
    // already has it allowing downloads and granting Anime instead of Movies.
    const serve = globalThis.fetch;
    const listed = { ...GROUP, download_allowed: true, library_ids: ["3"] };
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) =>
        String(input) === "/api/v2/admin/access-groups?limit=200"
          ? jsonResponse({ items: [listed], page: { has_more: false } })
          : serve(input, init),
      ),
    );
    adminUsers.data = [member(9, "robin", "user", null)];
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });

    await user.click(within(members).getByRole("button", { name: "Add users" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("checkbox"));
    await user.click(within(dialog).getByRole("button", { name: /Add 1 selected/ }));

    const confirm = await screen.findByRole("alertdialog");
    expect(within(confirm).getByText("Libraries: All libraries → Anime")).toBeInTheDocument();
    expect(within(confirm).queryByText(/Downloads:/)).not.toBeInTheDocument();
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
    // An ungrouped account inherits the no-group policy, so its losses are listed.
    expect(within(confirm).getByText("Downloads: Allowed → Not allowed")).toBeInTheDocument();
    expect(within(confirm).getByText("Concurrent streams: Unlimited → 1")).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Move" }));

    await waitFor(() => expect(adminUsers.update).toHaveBeenCalledTimes(1));
    expect(adminUsers.update.mock.calls[0]![0].user.id).toBe(9);
    expect(String(adminUsers.update.mock.calls[0]![1].access_group_id)).toBe("1");
    adminUsers.data = [];
  });

  it("waits for the group list before members can be moved or added", async () => {
    const serve = globalThis.fetch;
    let failGroups = true;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async (input, init) =>
        failGroups &&
        String(input) === "/api/v2/admin/access-groups?limit=200" &&
        (init?.method ?? "GET") === "GET"
          ? jsonResponse({ error: "unavailable", message: "down" }, 503)
          : serve(input, init),
      ),
    );
    adminUsers.data = [member(8, "sam", "user", 2)];
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });

    // Without the list, sam's group 2 can't be told apart from no group.
    expect(await within(members).findByText(/Could not load access groups/)).toBeInTheDocument();
    expect(within(members).getByRole("button", { name: "Add users" })).toBeDisabled();

    failGroups = false;
    await user.click(within(members).getByRole("button", { name: "Retry" }));
    await waitFor(() =>
      expect(within(members).getByRole("button", { name: "Add users" })).toBeEnabled(),
    );
    adminUsers.data = [];
  });

  it("names users that could not be added and keeps them for a retry", async () => {
    withGuestsGroup();
    adminUsers.data = [member(8, "sam", "user", 2)];
    adminUsers.update.mockReset().mockRejectedValueOnce(new Error("This user changed."));
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });

    await user.click(within(members).getByRole("button", { name: "Add users" }));
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("checkbox"));
    await user.click(within(dialog).getByRole("button", { name: /Add 1 selected/ }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Move" }),
    );

    const alert = await within(members).findByRole("alert");
    expect(alert).toHaveTextContent("Some users could not be added");
    expect(alert).toHaveTextContent("sam: This user changed.");
    // Reopening Add users has the failed user chosen, ready to retry.
    await user.click(within(members).getByRole("button", { name: "Add users" }));
    expect(within(await screen.findByRole("dialog")).getByRole("checkbox")).toBeChecked();
    adminUsers.data = [];
  });

  it("doesn't offer adding a retried user who has since joined the group", async () => {
    withGuestsGroup();
    adminUsers.data = [member(8, "sam", "user", 2)];
    adminUsers.update.mockReset().mockRejectedValueOnce(new Error("This user's group changed."));
    const user = userEvent.setup();
    renderPage("/admin/access-groups/1");
    const members = await screen.findByRole("region", { name: "Members" });

    await user.click(within(members).getByRole("button", { name: "Add users" }));
    await user.click(within(await screen.findByRole("dialog")).getByRole("checkbox"));
    await user.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: /Add 1 selected/ }),
    );
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Move" }),
    );
    await within(members).findByRole("alert");

    // sam is in this group now, so the retry has no one left to add.
    adminUsers.data = [member(8, "sam", "user", 1)];
    await user.click(within(members).getByRole("button", { name: "Add users" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: /Add\s+selected/ })).toBeDisabled();
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
});
