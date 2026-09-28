// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { v2, V2ProblemError } from "@/api/v2/request";
import { adminImportScope } from "@/api/v2/adminHistoryImports";
import { adminKeys } from "@/hooks/queries/keys";
import AdminHistoryImport from "./AdminHistoryImport";
const state = vi.hoisted(() => ({ profile: "owner", available: true }));
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
vi.mock("@/hooks/useAuth", () => ({ useOptionalAuth: () => ({ profile: { id: state.profile } }) }));
vi.mock("@/components/realtimeEventsContext", () => ({ useEventChannel: vi.fn() }));
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({ data: [{ id: 1, username: "Member" }] }),
}));
vi.mock("@/hooks/queries/admin/history", () => ({ useAdminUserProfiles: () => ({ data: [] }) }));
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
const source = {
  id: "1",
  name: "Saved server",
  source_type: "emby",
  base_url: "https://example.invalid",
  system_id: "",
  enabled: true,
  sort_order: 0,
  has_admin_token: true,
};
const mapping = {
  id: "2",
  source_id: "1",
  external_user_id: "external",
  external_user_name: "External",
  silo_user_id: "1",
  silo_profile_id: "profile",
};
function reply(options: unknown, body: unknown, etag = '"initial"') {
  (options as { onResponse?: (r: Response) => void })?.onResponse?.(
    new Response(null, { headers: { ETag: etag } }),
  );
  return Promise.resolve(body) as never;
}
function stale() {
  return new V2ProblemError(
    "edit",
    {
      type: "https://example.invalid/problems/precondition_failed",
      title: "Changed",
      status: 412,
      detail: "Changed",
      instance: "test",
    },
    null,
    '"not-for-auto-retry"',
  );
}
function baseline(operation: string, options: unknown) {
  if (operation === "GET /api/v2/admin/history-imports/capabilities")
    return reply(options, {
      available: state.available,
      guarded_configuration: true,
      durable_runs: true,
      max_bulk_mappings: 200,
    });
  if (operation === "GET /api/v2/admin/history-import-sources")
    return reply(options, { items: [source], page: { has_more: false } });
  if (operation === "GET /api/v2/admin/history-import-sources/{id}") return reply(options, source);
  if (operation === "GET /api/v2/admin/history-imports/mappings")
    return reply(options, { items: [mapping], page: { has_more: false } });
  if (operation === "GET /api/v2/admin/history-imports/mappings/{id}")
    return reply(options, mapping);
  if (operation === "GET /api/v2/admin/history-imports/runs")
    return reply(options, { items: [], page: { has_more: false } });
  throw new Error(operation);
}
function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  const tree = () => (
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AdminHistoryImport />
      </MemoryRouter>
    </QueryClientProvider>
  );
  const view = render(tree());
  return { client, ...view, refresh: () => view.rerender(tree()) };
}
beforeEach(() => {
  state.profile = "owner";
  state.available = true;
  vi.mocked(v2).mockImplementation(baseline);
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});
describe("admin history import editors", () => {
  it("shows failed mapping requests and retries without claiming there are no mappings", async () => {
    let fail = true;
    vi.mocked(v2).mockImplementation((operation, options) => {
      if (operation === "GET /api/v2/admin/history-imports/mappings" && fail)
        return Promise.reject(new Error("mapping database unavailable"));
      return baseline(operation, options);
    });
    mount();
    expect(await screen.findByText("User mappings could not be loaded.")).toBeTruthy();
    expect(screen.queryByText(/No user mappings yet/)).toBeNull();
    expect(screen.queryByText("Discover users")).toBeNull();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry user mappings" }));
    expect(await screen.findByText("External")).toBeTruthy();
    expect(screen.queryByText("User mappings could not be loaded.")).toBeNull();
  });

  it("shows loading while the selected source mappings are pending", async () => {
    vi.mocked(v2).mockImplementation((operation, options) => {
      if (operation === "GET /api/v2/admin/history-imports/mappings") return new Promise(() => {});
      return baseline(operation, options);
    });
    mount();
    expect(await screen.findByText("Loading user mappings…")).toBeTruthy();
    expect(screen.queryByText(/No user mappings yet/)).toBeNull();
  });

  it("preserves source draft and original tag on background refresh and412 until explicit reload", async () => {
    const consoleError = vi.spyOn(console, "error");
    let reads = 0;
    vi.mocked(v2).mockImplementation((operation, options) => {
      if (operation === "GET /api/v2/admin/history-import-sources/{id}")
        return reply(
          options,
          { ...source, name: ++reads === 1 ? "Saved server" : "Latest server" },
          reads === 1 ? '"initial"' : '"reloaded"',
        );
      if (operation === "PUT /api/v2/admin/history-import-sources/{id}")
        return Promise.reject(stale());
      return baseline(operation, options);
    });
    const { client } = mount();
    fireEvent.click(await screen.findByTitle("Edit server"));
    const name = screen.getByLabelText("Name");
    fireEvent.change(name, { target: { value: "My draft" } });
    act(() =>
      client.setQueryData(
        [...adminKeys.historyImportSources(), adminImportScope()],
        [{ ...source, id: 1, name: "Background", etag: '"background"' }],
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("alert");
    expect((name as HTMLInputElement).value).toBe("My draft");
    const writes = () =>
      vi
        .mocked(v2)
        .mock.calls.filter(([op]) => op === "PUT /api/v2/admin/history-import-sources/{id}");
    expect(writes()).toHaveLength(1);
    expect(writes()[0]![1]).toMatchObject({
      headers: { "If-Match": '"initial"' },
      body: { name: "My draft" },
    });
    fireEvent.click(screen.getByText("Reload latest version"));
    await waitFor(() =>
      expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("Latest server"),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(writes()).toHaveLength(2));
    expect(writes()[1]![1]).toMatchObject({ headers: { "If-Match": '"reloaded"' } });
    expect(consoleError.mock.calls.flat().join(" ")).not.toContain("same key");
    expect(screen.getAllByText("Import all")).toHaveLength(1);
    consoleError.mockRestore();
  });
  it.each([
    ["Delete server", "DELETE /api/v2/admin/history-import-sources/{id}", "Delete"],
    ["Remove mapping", "DELETE /api/v2/admin/history-imports/mappings/{id}", "Remove"],
  ])("keeps %s confirmation after412", async (title, operation, button) => {
    vi.mocked(v2).mockImplementation((op, options) =>
      op === operation ? Promise.reject(stale()) : baseline(op, options),
    );
    mount();
    fireEvent.click(await screen.findByTitle(title));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: button }));
    await within(dialog).findByRole("alert");
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(vi.mocked(v2).mock.calls.filter(([op]) => op === operation)).toHaveLength(1);
  });
  it("keeps token draft on412 and clears sensitive dialog state on authority change", async () => {
    vi.mocked(v2).mockImplementation((op, options) =>
      op === "PUT /api/v2/admin/history-imports/sources/{id}/token"
        ? Promise.reject(stale())
        : baseline(op, options),
    );
    const view = mount();
    fireEvent.click(await screen.findByTitle("Set API key"));
    const input = screen.getByLabelText("API key / Token");
    fireEvent.change(input, { target: { value: "new-secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("alert");
    expect((input as HTMLInputElement).value).toBe("new-secret");
    state.profile = "other";
    view.refresh();
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });
  it("shows every bulk outcome and gates unavailable capability", async () => {
    vi.mocked(v2).mockImplementation((op, options) =>
      op === "POST /api/v2/admin/history-imports/sources/{id}/bulk-run"
        ? reply(options, {
            accepted: 1,
            active: 1,
            failed: 1,
            outcomes: [
              { mapping_id: "2", status: "accepted" },
              { mapping_id: "3", status: "active" },
              { mapping_id: "4", status: "failed", error: "Unavailable source" },
            ],
          })
        : baseline(op, options),
    );
    const view = mount();
    fireEvent.click(await screen.findByText("Import all"));
    await screen.findByText("1 queued, 1 already active, 1 failed.");
    expect(screen.getByText("Mapping 4: failed — Unavailable source")).toBeTruthy();
    view.unmount();
    state.available = false;
    mount();
    await screen.findByRole("alert");
    expect(screen.queryByTitle("Edit server")).toBeNull();
  });
  it("loads run history one page at a time after an explicit request", async () => {
    let reads = 0;
    const run = {
      id: "run",
      user_id: "1",
      profile_id: "profile",
      source_type: "emby",
      connection_mode: "admin_token",
      status: "completed",
      terminal: true,
      cancelable: false,
      fetched: 1,
      matched: 1,
      unmatched: 0,
      progress_updated: 0,
      history_created: 1,
      watchlist_added: 0,
      favorites_imported: 0,
      skipped: 0,
      warnings: [],
      unmatched_samples: [],
      error_message: "",
      created_at: "2026-09-05T00:00:00Z",
    };
    vi.mocked(v2).mockImplementation((op, options) => {
      if (op === "GET /api/v2/admin/history-imports/runs")
        return reply(options, {
          items: [{ ...run, id: ++reads === 1 ? "new" : "old" }],
          page: reads === 1 ? { has_more: true, next_cursor: "older" } : { has_more: false },
        });
      return baseline(op, options);
    });
    mount();
    await screen.findByText("Load more imports");
    expect(reads).toBe(1);
    fireEvent.click(screen.getByText("Load more imports"));
    await waitFor(() => expect(reads).toBe(2));
    await waitFor(() => expect(screen.queryByText("Load more imports")).toBeNull());
    const calls = vi
      .mocked(v2)
      .mock.calls.filter(([op]) => op === "GET /api/v2/admin/history-imports/runs");
    expect(calls[1]![1]).toMatchObject({ query: { limit: 50, cursor: "older" } });
  });
  it("keeps the token-clear confirmation open on412", async () => {
    vi.mocked(v2).mockImplementation((op, options) =>
      op === "DELETE /api/v2/admin/history-imports/sources/{id}/token"
        ? Promise.reject(stale())
        : baseline(op, options),
    );
    mount();
    fireEvent.click(await screen.findByTitle("Set API key"));
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await screen.findByRole("alert");
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(
      vi
        .mocked(v2)
        .mock.calls.filter(
          ([op]) => op === "DELETE /api/v2/admin/history-imports/sources/{id}/token",
        ),
    ).toHaveLength(1);
  });
  it("states what a Plex admin import leaves out, beside the imports and in the server editor", async () => {
    const plex = { ...source, source_type: "plex" };
    vi.mocked(v2).mockImplementation((op, options) =>
      op === "GET /api/v2/admin/history-import-sources"
        ? reply(options, { items: [plex], page: { has_more: false } })
        : op === "GET /api/v2/admin/history-import-sources/{id}"
          ? reply(options, plex)
          : baseline(op, options),
    );
    mount();
    const limits = "Plex admin imports only bring over finished plays";
    const note = await screen.findByRole("note", { name: limits });
    expect(within(note).getByText("Resume points for titles still in progress")).toBeTruthy();
    expect(
      within(note).getByText("Titles, seasons, or shows marked as watched without playing them"),
    ).toBeTruthy();
    expect(within(note).getByText("Watchlists")).toBeTruthy();
    await screen.findByText("Import all");
    fireEvent.click(screen.getByTitle("Edit server"));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("note", { name: limits })).toBeTruthy();
  });
  it("shows no Plex import limits for other source types", async () => {
    mount();
    await screen.findByText("Import all");
    expect(screen.queryByRole("note")).toBeNull();
  });
  it("requires legacy source reconfiguration before imports", async () => {
    vi.mocked(v2).mockImplementation((op, options) =>
      op === "GET /api/v2/admin/history-import-sources/{id}"
        ? reply(options, { ...source, needs_reconfiguration: true })
        : baseline(op, options),
    );
    mount();
    await screen.findByText(/This source needs reconfiguration/);
    expect(screen.queryByText("Import all")).toBeNull();
    fireEvent.click(screen.getByTitle("Edit server"));
    await screen.findByText(/This saved address contains unsupported credential/);
  });
});
