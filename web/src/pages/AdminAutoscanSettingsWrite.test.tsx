import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import AdminAutoscan from "./AdminAutoscan";
vi.mock("@/pages/admin/autoscan/ConnectionsPanel", () => ({ default: () => null }));
vi.mock("@/pages/admin/autoscan/ActivityPanel", () => ({ default: () => null }));
vi.mock("@/pages/admin/autoscan/SourcesPanel", () => ({ default: () => null }));
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic");
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function setup() {
  let stored = { enabled: true, default_poll_interval_seconds: 300, debounce_seconds: 10 };
  let finish!: () => void;
  const writes: (typeof stored)[] = [];
  const reply = (body: unknown) =>
    new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  vi.stubGlobal(
    "fetch",
    vi.fn((_url: unknown, init: RequestInit) => {
      if (init.method !== "PUT") return Promise.resolve(reply(stored));
      const body = JSON.parse(String(init.body)) as typeof stored;
      writes.push(body);
      return new Promise<Response>((resolve) => {
        finish = () => {
          stored = body;
          resolve(reply({ settings: body, reschedule_state: "applied" }));
        };
      });
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  render(
    <MemoryRouter initialEntries={["/?tab=settings"]}>
      <QueryClientProvider client={client}>
        <AdminAutoscan />
      </QueryClientProvider>
    </MemoryRouter>,
  );
  return { writes, finish: () => act(async () => finish()) };
}
it.each([false, true])("explicit save keeps later edits=%s", async (changed) => {
  const f = setup();
  const input = await screen.findByLabelText("Default check interval (seconds)");
  fireEvent.change(input, { target: { value: "120" } });
  fireEvent.blur(input);
  expect(f.writes).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(f.writes).toHaveLength(1));
  if (changed) fireEvent.change(input, { target: { value: "240" } });
  await f.finish();
  await waitFor(() =>
    expect(
      (screen.getByLabelText("Default check interval (seconds)") as HTMLInputElement).value,
    ).toBe(changed ? "240" : "120"),
  );
  if (changed) {
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() => expect(f.writes).toHaveLength(2));
    expect(f.writes[1]?.default_poll_interval_seconds).toBe(240);
    await f.finish();
  }
});
it("does not submit a retained draft under a replacement authority", async () => {
  const f = setup();
  const input = await screen.findByLabelText("Default check interval (seconds)");
  fireEvent.change(input, { target: { value: "120" } });
  act(() => setProfileToken("pin-b"));
  // An observable local rerender resolves the current authority without remounting the form.
  fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
  await waitFor(() => expect(f.writes).toHaveLength(0));
});
