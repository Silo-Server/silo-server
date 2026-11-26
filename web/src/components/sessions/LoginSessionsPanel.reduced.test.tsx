// @vitest-environment jsdom
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { LoginSessionsPanel } from "./LoginSessionsPanel";

const mocks = vi.hoisted(() => ({ request: vi.fn() }));
vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({ profile: null, clearLoginSession: vi.fn() }),
}));
vi.mock("@/api/v2/request", () => ({ v2: mocks.request }));
afterEach(cleanup);

const own = {
  id: "own",
  device_name: "Firefox/1 Linux",
  ip_address: "192.0.2.1",
  created_at: "2026-10-01T00:00:00Z",
  expires_at: "2026-10-31T00:00:00Z",
  last_seen_at: "2026-10-02T00:00:00Z",
  current: true,
};
// A reduced item repeats its page's current_session times and carries nothing else.
const reducedOn = (id: string, current: typeof own) => ({
  id,
  device_name: "",
  ip_address: "",
  created_at: current.created_at,
  expires_at: current.expires_at,
  last_seen_at: null,
  current: false,
});
const full = (id: string, name: string) => ({
  id,
  device_name: name,
  ip_address: "198.51.100.4",
  created_at: "2026-09-01T00:00:00Z",
  expires_at: "2026-11-01T00:00:00Z",
  last_seen_at: null,
  current: false,
});

// A demo visitor's list carries another visitor's session by id only, with the
// caller's own times repeated; the row must not present them as that session's.
// A refresh between the two page requests moves the caller's expires_at, so
// each page's reduced items repeat a different current_session.
it("shows reduced sessions on every page without the caller's own times", async () => {
  const refreshed = { ...own, expires_at: "2026-11-30T00:00:00Z" };
  mocks.request.mockImplementation((_op: string, init: { query: { cursor?: string } }) =>
    Promise.resolve(
      init.query.cursor
        ? {
            items: [reducedOn("second-reduced", refreshed), full("second-full", "Edge/1 Windows")],
            current_session: refreshed,
            page: { has_more: false },
          }
        : {
            items: [own, reducedOn("first-reduced", own), full("first-full", "Chrome/1 Windows")],
            current_session: own,
            page: { has_more: true, next_cursor: "next" },
          },
    ),
  );
  const user = userEvent.setup();
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter>
        <LoginSessionsPanel manageOthers={false} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  await user.click(await screen.findByRole("button", { name: "Load more sessions" }));
  await screen.findByText("Edge on Windows");

  const reducedRows = screen.getAllByText("Unknown device").map((el) => el.closest("li")!);
  expect(reducedRows).toHaveLength(2);
  for (const row of reducedRows) {
    expect(within(row).queryByRole("button", { name: /Details/ })).toBeNull();
    expect(row).not.toHaveTextContent(/Signed in|Last seen|Expires|2026|Not sent|Unknown$/);
  }
  // The current row and a full row on either page render as before.
  for (const name of ["Chrome on Windows", "Edge on Windows", "Firefox on Linux"]) {
    const row = screen.getByText(name).closest("li")!;
    expect(row).toHaveTextContent("Signed in");
    await user.click(within(row).getByRole("button", { name: /Details/ }));
    expect(row).toHaveTextContent("Expires");
  }
  for (const name of ["Chrome on Windows", "Edge on Windows"])
    expect(screen.getByText(name).closest("li")!).toHaveTextContent(
      "Last seen not recorded yet · Signed in",
    );
  expect(screen.getByText("Firefox on Linux").closest("li")!).toHaveTextContent("192.0.2.1");
});
