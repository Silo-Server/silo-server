import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import AdminAutoscan from "./AdminAutoscan";
vi.mock("@/pages/admin/autoscan/ConnectionsPanel", () => ({ default: () => null }));
vi.mock("@/pages/admin/autoscan/ActivityPanel", () => ({ default: () => null }));
vi.mock("@/pages/admin/autoscan/SourcesPanel", () => ({ default: () => null }));
const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
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
const pollSource = {
  id: "poll",
  plugin_id: "plugin",
  capability_id: "scan_source",
  connection_id: null,
  enabled: true,
  delivery_mode: "poll",
  poll_interval_seconds: null,
  path_rewrites: [],
  source_config: {},
  label: "Source",
  last_run_at: null,
  last_error: null,
  webhook_configured: false,
};
function setup({
  sources = [pollSource] as unknown[],
  enabled = true,
  sourcesFail = false,
}: { sources?: unknown[]; enabled?: boolean; sourcesFail?: boolean } = {}) {
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
      if (String(url).includes("/admin/autoscan/sources")) {
        if (sourcesFail) return Promise.resolve(new Response(null, { status: 500 }));
        return Promise.resolve(reply({ items: sources, page: { has_more: false } }));
      }
      return Promise.resolve(
        reply({ enabled, default_poll_interval_seconds: 300, debounce_seconds: 10 }),
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
    reject: (status: number, problem: Record<string, unknown>) =>
      act(async () =>
        release(
          new Response(JSON.stringify({ status, ...problem }), {
            status,
            headers: { "Content-Type": "application/problem+json" },
          }),
        ),
      ),
  };
}
it("Run now remains pending until process reservation returns and reports start, not completion", async () => {
  const f = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Run now" }));
  await waitFor(() => expect(f.commands).toHaveLength(1));
  expect(f.commands[0]).toContain("/api/v2/admin/autoscan/trigger");
  expect(screen.getByRole("button", { name: "Triggering…" })).toBeDisabled();
  expect(toast.success).not.toHaveBeenCalled();
  await f.finish();
  // Run now has no cooldown: it is available again as soon as the start returns.
  await waitFor(() => expect(screen.getByRole("button", { name: "Run now" })).toBeEnabled());
  expect(toast.success).toHaveBeenCalledWith(
    "Autoscan poll started on this server process. Check activity for source outcomes.",
  );
});
it("late result after PIN replacement never reports old command success", async () => {
  const f = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Run now" }));
  await waitFor(() => expect(f.commands).toHaveLength(1));
  act(() => setProfileToken("pin-b"));
  await f.finish();
  await waitFor(() => expect(screen.getByRole("button", { name: "Run now" })).toBeEnabled());
  expect(toast.success).not.toHaveBeenCalled();
  expect(toast.error).not.toHaveBeenCalled();
});
it("uncertain response reports reconciliation and does not dispatch a replacement", async () => {
  const f = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Run now" }));
  await waitFor(() => expect(f.commands).toHaveLength(1));
  await f.fail();
  await waitFor(() =>
    expect(toast.error).toHaveBeenCalledWith(
      "Autoscan start could not be confirmed. Check task and activity state before running again.",
    ),
  );
  expect(f.commands).toHaveLength(1);
});

it("says a poll is already running when the server refuses a second start", async () => {
  const f = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Run now" }));
  await waitFor(() => expect(f.commands).toHaveLength(1));
  await f.reject(409, {
    type: "https://siloserver.org/docs/api/v2/problems/conflict",
    title: "Conflict",
    detail: "Task is already running on this process",
  });
  await waitFor(() =>
    expect(toast.info).toHaveBeenCalledWith(
      "A poll is already running and may skip recently polled sources. Press Run now again when it finishes to poll every source.",
    ),
  );
  expect(toast.error).not.toHaveBeenCalled();
  expect(f.commands).toHaveLength(1);
  await waitFor(() => expect(screen.getByRole("button", { name: "Run now" })).toBeEnabled());
});

it("keeps the uncertain message for any other refusal", async () => {
  const f = setup();
  fireEvent.click(await screen.findByRole("button", { name: "Run now" }));
  await waitFor(() => expect(f.commands).toHaveLength(1));
  await f.reject(503, {
    type: "https://siloserver.org/docs/api/v2/problems/service_unavailable",
    title: "Service Unavailable",
    detail: "admin tasks unavailable",
  });
  await waitFor(() =>
    expect(toast.error).toHaveBeenCalledWith(
      "Autoscan start could not be confirmed. Check task and activity state before running again.",
    ),
  );
  expect(toast.info).not.toHaveBeenCalled();
});

it("hides Run now when no polling source is enabled", async () => {
  setup({ sources: [{ ...pollSource, delivery_mode: "webhook", webhook_configured: true }] });
  await screen.findByText("Enabled");
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "Run now" })).not.toBeInTheDocument(),
  );
});

it("keeps Run now when the source list fails to load", async () => {
  setup({ sourcesFail: true });
  await screen.findByText("Enabled");
  await waitFor(() =>
    expect(fetch).toHaveBeenCalledWith(
      expect.stringContaining("/admin/autoscan/sources"),
      expect.anything(),
    ),
  );
  expect(await screen.findByRole("button", { name: "Run now" })).toBeEnabled();
});

it("hides Run now while Autoscan is off", async () => {
  setup({ enabled: false });
  await screen.findByText("Disabled");
  expect(screen.queryByRole("button", { name: "Run now" })).not.toBeInTheDocument();
});

it("keeps Run now enabled right after a recent poll", async () => {
  setup({
    sources: [{ ...pollSource, last_run_at: new Date(Date.now() - 30_000).toISOString() }],
  });
  const button = await screen.findByRole("button", { name: "Run now" });
  expect(button).toBeEnabled();
  expect(button).not.toHaveAttribute("aria-describedby");
});
