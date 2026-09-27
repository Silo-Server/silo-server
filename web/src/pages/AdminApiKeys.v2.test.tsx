// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { v2, V2ProblemError } from "@/api/v2/request";
import AdminApiKeys from "./AdminApiKeys";
const state = vi.hoisted(() => ({
  profile: "owner",
  viewer: 2,
  users: [{ id: 2, username: "Admin", is_owner: false }],
  usersPending: false,
  usersFailed: false,
}));
vi.mock("@/api/v2/request", async (original) => ({
  ...(await original<typeof import("@/api/v2/request")>()),
  v2: vi.fn(),
}));
vi.mock("@/api/client", async (original) => ({
  ...(await original<typeof import("@/api/client")>()),
  captureProfileRequestContext: () => ({
    accessToken: "test",
    authContextVersion: 1,
    serverOrigin: "",
    profileId: state.profile,
    profileToken: null,
  }),
  isCapturedProfileAuthorityActive: (c: { profileId: string }) => c.profileId === state.profile,
}));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ user: { id: state.viewer }, profile: { id: state.profile } }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () =>
    state.usersPending
      ? { data: undefined, isPending: true, isError: false }
      : state.usersFailed
        ? { data: undefined, isPending: false, isError: true }
        : { data: state.users, isPending: false, isError: false },
}));
const row = {
  id: "7",
  user_id: "2",
  label: "Automation",
  key_prefix: "sa_12345678",
  rate_tier: "standard",
  scopes: [],
  created_at: "2026-01-01T00:00:00.000Z",
  username: "Admin",
};
function reply(options: unknown, body: unknown, headers: Record<string, string> = {}) {
  (options as { onResponse?: (r: Response) => void }).onResponse?.(new Response(null, { headers }));
  return Promise.resolve(body) as never;
}
function baseline(op: string, options: unknown) {
  if (op.endsWith("/capabilities"))
    return reply(options, {
      available: true,
      guarded_configuration: true,
      scopes: [],
      rate_tiers: ["standard", "elevated"],
    });
  if (op === "GET /api/v2/admin/api-keys")
    return reply(options, { items: [row], page: { has_more: false } });
  if (op === "GET /api/v2/admin/api-keys/{id}") return reply(options, row, { ETag: '"initial"' });
  throw new Error(op);
}
function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  const tree = () => (
    <QueryClientProvider client={client}>
      <AdminApiKeys />
    </QueryClientProvider>
  );
  const view = render(tree());
  return { client, ...view, refresh: () => view.rerender(tree()) };
}
beforeEach(() => {
  HTMLElement.prototype.scrollIntoView = vi.fn();
  state.profile = "owner";
  state.viewer = 2;
  state.users = [{ id: 2, username: "Admin", is_owner: false }];
  state.usersPending = false;
  state.usersFailed = false;
  vi.mocked(v2).mockImplementation(baseline);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
it("clears creation-only secret and mutation data on close", async () => {
  vi.mocked(v2).mockImplementation((op, options) =>
    op === "POST /api/v2/admin/api-keys"
      ? reply(
          options,
          { ...row, key: "creation-only-secret" },
          { Location: "/api/v2/admin/api-keys/7" },
        )
      : baseline(op, options),
  );
  const { client } = mount();
  await screen.findByText("Automation");
  expect(screen.queryByRole("button", { name: /Copy API key/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Create Key" }));
  fireEvent.change(screen.getByLabelText("Label"), { target: { value: "New" } });
  fireEvent.click(screen.getByRole("button", { name: "Create" }));
  await screen.findByText("creation-only-secret");
  await waitFor(() =>
    expect(
      JSON.stringify(
        client
          .getMutationCache()
          .getAll()
          .map((m) => m.state.data),
      ),
    ).not.toContain("creation-only-secret"),
  );
  fireEvent.click(screen.getByRole("button", { name: "Close" }));
  expect(screen.queryByText("creation-only-secret")).toBeNull();
  expect(
    JSON.stringify(
      client
        .getQueryCache()
        .getAll()
        .map((q) => q.state.data),
    ),
  ).not.toContain("creation-only-secret");
});
it("preserves tier draft on412 and changes tag only after explicit reload", async () => {
  let reads = 0;
  vi.mocked(v2).mockImplementation((op, options) => {
    if (op === "GET /api/v2/admin/api-keys/{id}")
      return reply(options, row, { ETag: ++reads === 1 ? '"initial"' : '"reloaded"' });
    if (op === "PUT /api/v2/admin/api-keys/{id}/tier")
      return Promise.reject(
        new V2ProblemError(
          "update",
          {
            type: "https://example.invalid/precondition_failed",
            title: "Changed",
            status: 412,
            detail: "Changed",
            instance: "test",
          },
          null,
          '"conflict"',
        ),
      );
    return baseline(op, options);
  });
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Edit rate limit for Automation" }));
  await screen.findByRole("dialog");
  fireEvent.click(screen.getByRole("combobox", { name: "New rate limit" }));
  fireEvent.click(await screen.findByRole("option", { name: "Elevated" }));
  fireEvent.click(screen.getByRole("button", { name: "Save rate limit" }));
  await screen.findByRole("alert");
  expect(screen.getByRole("combobox", { name: "New rate limit" }).textContent).toContain(
    "Elevated",
  );
  const writes = () =>
    vi.mocked(v2).mock.calls.filter(([op]) => op === "PUT /api/v2/admin/api-keys/{id}/tier");
  expect(writes()).toHaveLength(1);
  expect(writes()[0]![1]).toMatchObject({
    headers: { "If-Match": '"initial"' },
    body: { rate_tier: "elevated" },
    retryAuthentication: false,
  });
  fireEvent.click(screen.getByRole("button", { name: "Reload current key" }));
  await waitFor(() =>
    expect(
      (screen.getByRole("button", { name: "Save rate limit" }) as HTMLButtonElement).disabled,
    ).toBe(false),
  );
  expect(screen.getByRole("combobox", { name: "New rate limit" }).textContent).toContain(
    "Elevated",
  );
  fireEvent.click(screen.getByRole("button", { name: "Save rate limit" }));
  await waitFor(() => expect(writes()).toHaveLength(2));
  expect(writes()[1]![1]).toMatchObject({ headers: { "If-Match": '"reloaded"' } });
});
it("retains failed revocation and never replays it", async () => {
  vi.mocked(v2).mockImplementation((op, options) =>
    op.startsWith("DELETE") ? Promise.reject(new Error("Connection lost")) : baseline(op, options),
  );
  mount();
  fireEvent.click(await screen.findByRole("button", { name: "Revoke API key Automation" }));
  const dialog = await screen.findByRole("dialog");
  fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }));
  await screen.findByRole("alert");
  expect(screen.getByText("Automation", { selector: "td" })).toBeTruthy();
  expect(vi.mocked(v2).mock.calls.filter(([op]) => op.startsWith("DELETE"))).toHaveLength(1);
});
it("discards the editor on profile change", async () => {
  const view = mount();
  fireEvent.click(await screen.findByRole("button", { name: "Edit rate limit for Automation" }));
  await screen.findByRole("dialog");
  state.profile = "other";
  view.refresh();
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});
it("labels the key's tier as a rate limit and shows each option's limits", async () => {
  vi.mocked(v2).mockImplementation((op, options) => {
    if (op === "GET /api/v2/admin/rate-limits/config")
      return reply(
        options,
        {
          enabled: true,
          tiers: {
            standard: { requests_per_second: 20, requests_per_minute: 1200, burst: 20 },
            elevated: { requests_per_second: 100, requests_per_minute: 6000, burst: 100 },
          },
        },
        { ETag: '"limits"' },
      );
    if (op === "GET /api/v2/admin/rate-limits/status") return reply(options, { active: true });
    return baseline(op, options);
  });
  mount();
  const table = await screen.findByRole("table");
  expect(within(table).getByRole("columnheader", { name: "Rate limit" })).toBeTruthy();
  expect(within(table).queryByRole("columnheader", { name: "Tier" })).toBeNull();
  expect(within(table).getByRole("cell", { name: "Standard" })).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "Edit rate limit for Automation" }));
  const dialog = await screen.findByRole("dialog");
  expect(within(dialog).getByText("Edit API key rate limit")).toBeTruthy();
  expect(within(dialog).getByText(/current rate limit: Standard/)).toBeTruthy();
  expect(within(dialog).getByText(/doesn't change what the key can access/)).toBeTruthy();
  await waitFor(() =>
    expect(within(dialog).getByRole("combobox", { name: "New rate limit" }).textContent).toContain(
      "Standard — 20 requests/s, 1,200/min",
    ),
  );
  expect(within(dialog).getByRole("button", { name: "Save rate limit" })).toBeTruthy();
});

it("offers the Owner's keys only to the Owner", async () => {
  state.users = [
    { id: 2, username: "Admin", is_owner: true },
    { id: 3, username: "Other", is_owner: false },
  ];
  state.viewer = 3;
  mount();
  await screen.findByText("Automation");
  expect(screen.queryByRole("button", { name: "Edit rate limit for Automation" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Revoke API key Automation" })).toBeNull();
  cleanup();

  state.viewer = 2;
  mount();
  await screen.findByText("Automation");
  expect(screen.getByRole("button", { name: "Edit rate limit for Automation" })).toBeEnabled();
  expect(screen.getByRole("button", { name: "Revoke API key Automation" })).toBeEnabled();
});

it.each([
  ["loads", "usersPending"],
  ["fails to load", "usersFailed"],
] as const)("offers no key actions while the account list %s", async (_, flag) => {
  state[flag] = true;
  mount();
  await screen.findByText("Automation");
  expect(screen.queryByRole("button", { name: "Edit rate limit for Automation" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Revoke API key Automation" })).toBeNull();
});
