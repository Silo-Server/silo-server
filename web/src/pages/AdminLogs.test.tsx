// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { v2 } from "@/api/v2/request";
import { useAdminLogStream } from "@/hooks/admin/useAdminLogStream";
import AdminLogs from "./AdminLogs";

vi.mock("@/api/v2/request", () => ({ v2: vi.fn() }));
vi.mock("@/api/client", () => ({
  captureProfileRequestContext: () => ({ profileId: "owner", authContextVersion: 1 }),
  isCapturedProfileAuthorityActive: () => true,
  StaleApiRequestContextError: class extends Error {},
}));
vi.mock("@/hooks/useAuth", () => ({ useOptionalAuth: () => null }));
vi.mock("@/hooks/useDateTimeFormat", () => ({ useDateTimeFormat: () => "locale" }));
vi.mock("@/hooks/admin/useAdminLogStream", () => ({ useAdminLogStream: vi.fn() }));

function page(id: string, hasMore: boolean) {
  return {
    items: [
      {
        id,
        timestamp: "2026-09-09T00:00:00Z",
        level: "info",
        component: "test",
        message: `Log ${id}`,
      },
    ],
    page: { has_more: hasMore, next_cursor: hasMore ? `cursor-${id}` : null },
  };
}
function mount(path = "/admin/logs?q=needle") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <AdminLogs />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
beforeEach(() => {
  vi.mocked(useAdminLogStream).mockReturnValue({
    rows: [],
    nextCursor: "old-live-cursor",
    isConnecting: false,
    isLive: true,
    connectionState: "live",
    reconnect: vi.fn(),
  });
  vi.mocked(v2).mockImplementation(
    async (_operation, options) =>
      page(
        options?.query && "cursor" in options.query && options.query.cursor ? "1" : "2",
        !(options?.query && "cursor" in options.query && options.query.cursor),
      ) as never,
  );
});
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("admin log history", () => {
  it("starts from a fresh snapshot, pages with server cursors, and returns to live logs", async () => {
    mount("/admin/logs?q=needle&level=error&component=scanner");
    await waitFor(() =>
      expect(latestAppStreamParams()).toMatchObject({
        level: "error",
        component: "scanner",
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Browse log history" }));
    expect(await screen.findByText("Log 2")).toBeTruthy();
    expect(vi.mocked(v2).mock.calls[0]).toMatchObject([
      "GET /api/v2/admin/logs/app",
      { query: { q: "needle", level: "error", component: "scanner", cursor: undefined } },
    ]);
    expect(vi.mocked(useAdminLogStream).mock.calls.at(-2)?.[2]).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Older" }));
    expect(await screen.findByText("Log 1")).toBeTruthy();
    expect(vi.mocked(v2).mock.calls.at(-1)).toMatchObject([
      "GET /api/v2/admin/logs/app",
      { query: { q: "needle", cursor: "cursor-2" } },
    ]);
    expect((screen.getByRole("button", { name: "Older" }) as HTMLButtonElement).disabled).toBe(
      true,
    );
    fireEvent.click(screen.getByRole("button", { name: "Newer" }));
    expect(await screen.findByText("Log 2")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Return to live logs" }));
    expect(screen.getByRole("button", { name: "Browse log history" })).toBeTruthy();
    expect(vi.mocked(useAdminLogStream).mock.calls.at(-2)?.[2]).toBe(true);
  });

  it("resets history when a filter changes", async () => {
    mount();
    fireEvent.click(screen.getByRole("button", { name: "Browse log history" }));
    await screen.findByText("Log 2");
    fireEvent.click(screen.getByRole("button", { name: "Older" }));
    await screen.findByText("Log 1");
    const filter = screen.getByPlaceholderText("Message contains...");
    filter.focus();
    fireEvent.change(filter, { target: { value: "changed" } });
    expect(document.activeElement).toBe(filter);
    expect(screen.getByRole("button", { name: "Browse log history" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Browse log history" }));
    await waitFor(() =>
      expect(vi.mocked(v2).mock.calls.at(-1)).toMatchObject([
        "GET /api/v2/admin/logs/app",
        { query: { q: "changed", cursor: undefined } },
      ]),
    );
  });

  it("pages the audit stream and reports a failed fetch without an empty-state claim", async () => {
    let failed = true;
    vi.mocked(v2).mockImplementation(async () => {
      if (failed) throw new Error("database unavailable");
      return { items: [], page: { has_more: false } } as never;
    });
    mount("/admin/logs?tab=audit&method=POST&client_ip=192.0.2.1");
    fireEvent.click(screen.getByRole("button", { name: "Browse log history" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.queryByText("No audit logs matched the current filters.")).toBeNull();
    expect(vi.mocked(v2).mock.calls[0]).toMatchObject([
      "GET /api/v2/admin/logs/audit",
      { query: { method: "POST", client_ip: "192.0.2.1", cursor: undefined } },
    ]);
    failed = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry log history" }));
    expect(await screen.findByText("No audit logs matched the current filters.")).toBeTruthy();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

function latestAppStreamParams() {
  const call = vi
    .mocked(useAdminLogStream)
    .mock.calls.filter((entry) => entry[0] === "app")
    .at(-1);
  return call?.[1] as { level?: string; component?: string } | undefined;
}

describe("admin log level and component filters", () => {
  beforeEach(() => {
    // Radix Select uses Pointer Capture APIs that jsdom does not implement.
    if (!Element.prototype.hasPointerCapture) {
      Element.prototype.hasPointerCapture = () => false;
    }
    if (!Element.prototype.setPointerCapture) {
      Element.prototype.setPointerCapture = () => undefined;
    }
    if (!Element.prototype.releasePointerCapture) {
      Element.prototype.releasePointerCapture = () => undefined;
    }
    if (!Element.prototype.scrollIntoView) {
      Element.prototype.scrollIntoView = () => undefined;
    }
  });

  it("treats a reserved all URL value as no level or component filter", async () => {
    const user = userEvent.setup();
    mount("/admin/logs?level=all&component=all");
    await waitFor(() => {
      const params = latestAppStreamParams();
      expect(params?.level).toBeUndefined();
      expect(params?.component).toBeUndefined();
    });
    await user.click(screen.getByRole("combobox", { name: "Level" }));
    expect(screen.getAllByRole("option", { name: "All levels" })).toHaveLength(1);
    expect(screen.queryByRole("option", { name: /^all$/ })).toBeNull();
    await user.keyboard("{Escape}");
    expect(screen.getByRole("combobox", { name: "Component" })).toHaveValue("all");
  });

  it("updates stream params from the filters and clears them", async () => {
    const user = userEvent.setup();
    mount("/admin/logs");

    await user.click(screen.getByRole("combobox", { name: "Level" }));
    await user.click(await screen.findByRole("option", { name: "error" }));
    await waitFor(() => expect(latestAppStreamParams()).toMatchObject({ level: "error" }));

    const component = screen.getByRole("combobox", { name: "Component" });
    const suggestions = (component as HTMLInputElement).list;
    expect(Array.from(suggestions?.options ?? []).map((option) => option.value)).toContain(
      "scanner",
    );
    await user.type(component, "scanner");
    await waitFor(() =>
      expect(latestAppStreamParams()).toMatchObject({
        level: "error",
        component: "scanner",
      }),
    );

    await user.click(screen.getByRole("combobox", { name: "Level" }));
    await user.click(await screen.findByRole("option", { name: "All levels" }));
    await user.clear(component);
    await waitFor(() => {
      const params = latestAppStreamParams();
      expect(params?.level).toBeUndefined();
      expect(params?.component).toBeUndefined();
    });
  });

  it.each(["apiv2", "subtitles", "notifications.fanout", "future.worker", "allworkers"])(
    "filters live and history logs by %s, including after clearing and re-entering it",
    async (value) => {
      const user = userEvent.setup();
      mount("/admin/logs?level=error");
      const component = screen.getByRole("combobox", { name: "Component" });

      for (let attempt = 0; attempt < 2; attempt++) {
        await user.type(component, value);
        expect(component).toHaveValue(value);
        await waitFor(() =>
          expect(latestAppStreamParams()).toMatchObject({ level: "error", component: value }),
        );
        await user.click(screen.getByRole("button", { name: "Browse log history" }));
        await waitFor(() =>
          expect(vi.mocked(v2).mock.calls.at(-1)).toMatchObject([
            "GET /api/v2/admin/logs/app",
            { query: { level: "error", component: value, cursor: undefined } },
          ]),
        );
        await user.clear(component);
        expect(screen.getByRole("button", { name: "Browse log history" })).toBeTruthy();
        await waitFor(() => expect(latestAppStreamParams()?.component).toBeUndefined());
      }
    },
  );
});

it("shows the actor, affected account and safe permission/password changes", () => {
  vi.mocked(useAdminLogStream).mockReturnValue({
    rows: [
      {
        id: 900,
        timestamp: "2026-10-08T10:00:00Z",
        client_ip: "192.0.2.1",
        method: "PUT",
        path: "/api/v2/admin/users/2",
        status_code: 204,
        duration_ms: 1,
        user_id: 1,
        action: "user.updated",
        target_type: "user",
        target_id: "2",
        changes: [
          { field: "permissions", before: "[]", after: '["marker_edit"]' },
          { field: "password" },
        ],
      },
    ],
    isConnecting: false,
    isLive: true,
    connectionState: "live",
    reconnect: vi.fn(),
  });
  mount("/admin/logs?tab=audit&action=user.updated&actor_user_id=1&target_type=user&target_id=2");
  expect(screen.getByText("user.updated")).toBeInTheDocument();
  expect(screen.getByText("user #2")).toBeInTheDocument();
  expect(screen.getByText(/none → marker_edit/)).toBeInTheDocument();
  expect(screen.getByText("password").parentElement).toHaveTextContent("password: changed");
  expect(vi.mocked(useAdminLogStream).mock.calls.at(-1)?.[1]).toMatchObject({
    action: "user.updated",
    actor_user_id: "1",
    target_type: "user",
    target_id: "2",
  });
});

it.each([
  ["access_group", "library_ids", "[]", "all libraries → none"],
  ["access_group", "allowed_permissions", "[]", "all assignable → none"],
  ["user", "access_group_id", "5", "none → 5"],
  ["user", "max_streams", "3", "inherit → 3"],
])("shows the meaning of null for %s %s", (targetType, field, after, expected) => {
  vi.mocked(useAdminLogStream).mockReturnValue({
    rows: [
      {
        id: 901,
        timestamp: "2026-10-08T10:00:00Z",
        client_ip: "192.0.2.1",
        method: "PUT",
        path: "/api/v2/admin/users/2",
        status_code: 204,
        duration_ms: 1,
        user_id: 1,
        action: `${targetType}.updated`,
        target_type: targetType,
        target_id: "2",
        changes: [{ field, before: "null", after }],
      },
    ],
    isConnecting: false,
    isLive: true,
    connectionState: "live",
    reconnect: vi.fn(),
  });
  mount("/admin/logs?tab=audit");
  expect(screen.getByText(field.replaceAll("_", " ")).parentElement).toHaveTextContent(expected);
});

it("shows object change values as JSON", () => {
  vi.mocked(useAdminLogStream).mockReturnValue({
    rows: [
      {
        id: 902,
        timestamp: "2026-10-08T10:00:00Z",
        client_ip: "192.0.2.1",
        method: "PUT",
        path: "/api/v2/admin/users/2",
        status_code: 204,
        duration_ms: 1,
        user_id: 1,
        action: "user.updated",
        target_type: "user",
        target_id: "2",
        changes: [{ field: "max_streams", before: '{"a":1}', after: "2" }],
      },
    ],
    isConnecting: false,
    isLive: true,
    connectionState: "live",
    reconnect: vi.fn(),
  });
  mount("/admin/logs?tab=audit");
  expect(screen.getByText("max streams").parentElement).toHaveTextContent('{"a":1} → 2');
});
