import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import AdminAutoscan from "./AdminAutoscan";
vi.mock("@/pages/admin/autoscan/ConnectionsPanel", () => ({ default: () => null }));
vi.mock("@/pages/admin/autoscan/ActivityPanel", () => ({ default: () => null }));
vi.mock("@/pages/admin/autoscan/SourcesPanel", () => ({ default: () => null }));
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast }));
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic");
  setProfileId("a");
  setProfileToken("pin-a");
  vi.clearAllMocks();
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function setup() {
  let release!: (response: Response) => void;
  const commands: string[] = [];
  const reply = (body: unknown) =>
    new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  vi.stubGlobal(
    "fetch",
    vi.fn((url: unknown, init: RequestInit) => {
      if (init.method === "POST") {
        commands.push(String(url));
        return new Promise<Response>((resolve) => {
          release = resolve;
        });
      }
      return Promise.resolve(
        reply({ enabled: true, default_poll_interval_seconds: 300, debounce_seconds: 10 }),
      );
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <AdminAutoscan />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return {
    commands,
    finish: () =>
      act(async () =>
        release(reply({ key: "autoscan_poll", state: "running", execution_scope: "process" })),
      ),
    fail: () => act(async () => release(new Response(null, { status: 500 }))),
  };
}
it("Run now remains pending until process reservation returns and reports start, not completion", async () => {
  const f = setup();
  fireEvent.click(screen.getByRole("button", { name: "Run now" }));
  await waitFor(() => expect(f.commands).toHaveLength(1));
  expect(f.commands[0]).toContain("/api/v2/admin/autoscan/trigger");
  expect(screen.getByRole("button", { name: "Triggering…" })).toBeDisabled();
  expect(toast.success).not.toHaveBeenCalled();
  await f.finish();
  await waitFor(() => expect(screen.getByRole("button", { name: "Run now" })).toBeEnabled());
  expect(toast.success).toHaveBeenCalledWith(
    "Autoscan poll started on this server process. Check activity for source outcomes.",
  );
});
it("late result after PIN replacement never reports old command success", async () => {
  const f = setup();
  fireEvent.click(screen.getByRole("button", { name: "Run now" }));
  await waitFor(() => expect(f.commands).toHaveLength(1));
  act(() => setProfileToken("pin-b"));
  await f.finish();
  await waitFor(() => expect(screen.getByRole("button", { name: "Run now" })).toBeEnabled());
  expect(toast.success).not.toHaveBeenCalled();
  expect(toast.error).not.toHaveBeenCalled();
});
it("uncertain response reports reconciliation and does not dispatch a replacement", async () => {
  const f = setup();
  fireEvent.click(screen.getByRole("button", { name: "Run now" }));
  await waitFor(() => expect(f.commands).toHaveLength(1));
  await f.fail();
  await waitFor(() =>
    expect(toast.error).toHaveBeenCalledWith(
      "Autoscan start could not be confirmed. Check task and activity state before running again.",
    ),
  );
  expect(f.commands).toHaveLength(1);
});
