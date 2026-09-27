// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
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
    mount();
    fireEvent.click(screen.getByRole("button", { name: "Browse log history" }));
    expect(await screen.findByText("Log 2")).toBeTruthy();
    expect(vi.mocked(v2).mock.calls[0]).toMatchObject([
      "GET /api/v2/admin/logs/app",
      { query: { q: "needle", cursor: undefined } },
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
