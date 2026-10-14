import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import AutoscanSettings from "./AutoscanSettings";
vi.mock("@/pages/admin/autoscan/ConnectionsPanel", () => ({ default: () => null }));
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
  let failReads = false;
  const writes: (typeof stored)[] = [];
  const reply = (body: unknown) =>
    new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  vi.stubGlobal(
    "fetch",
    vi.fn((_url: unknown, init: RequestInit) => {
      if (init.method !== "PUT") {
        return Promise.resolve(failReads ? new Response("", { status: 500 }) : reply(stored));
      }
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
  // A fresh element each time, so a rerender reaches the page.
  const tree = () => (
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <AutoscanSettings />
      </QueryClientProvider>
    </MemoryRouter>
  );
  const { rerender } = render(tree());
  return {
    writes,
    client,
    failReads: () => {
      failReads = true;
    },
    finish: () => act(async () => finish()),
    rerender: () => rerender(tree()),
  };
}
it.each([false, true])("explicit save keeps later edits=%s", async (changed) => {
  const f = setup();
  const input = await screen.findByLabelText("Default check interval");
  fireEvent.change(input, { target: { value: "120" } });
  fireEvent.blur(input);
  expect(f.writes).toHaveLength(0);
  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  await waitFor(() => expect(f.writes).toHaveLength(1));
  if (changed) fireEvent.change(input, { target: { value: "240" } });
  await f.finish();
  await waitFor(() =>
    expect((screen.getByLabelText("Default check interval") as HTMLInputElement).value).toBe(
      changed ? "240" : "120",
    ),
  );
  if (changed) {
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(f.writes).toHaveLength(2));
    expect(f.writes[1]?.default_poll_interval_seconds).toBe(240);
    await f.finish();
  }
});
it("does not submit a retained draft under a replacement authority", async () => {
  const f = setup();
  const input = await screen.findByLabelText("Default check interval");
  fireEvent.change(input, { target: { value: "120" } });
  expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();
  act(() => setProfileToken("pin-b"));
  // A rerender resolves the current authority without remounting the form. The
  // draft belonged to the old profile, so there is nothing left to save.
  f.rerender();
  const reloaded = (await screen.findByLabelText("Default check interval")) as HTMLInputElement;
  expect(reloaded.value).toBe("300");
  expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  expect(f.writes).toHaveLength(0);
});

it("does not send a draft hidden by a failed refetch", async () => {
  const f = setup();
  const input = await screen.findByLabelText("Default check interval");
  fireEvent.change(input, { target: { value: "120" } });
  f.failReads();
  await act(() => f.client.refetchQueries());
  await screen.findByRole("alert");
  expect(screen.queryByLabelText("Default check interval")).toBeNull();

  fireEvent.click(screen.getByRole("button", { name: "Save" }));
  // The write would go out on a later tick; let it.
  await act(() => new Promise((resolve) => setTimeout(resolve, 50)));
  expect(f.writes).toHaveLength(0);
});
