// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import HistoryImportSettings from "./HistoryImportSettings";
import { useHistoryImportRun } from "@/hooks/queries/history-import";
import { V2ProblemError } from "@/api/v2/request";
const state = vi.hoisted(() => ({
  refresh: vi.fn(),
  login: vi.fn(),
  createRun: vi.fn(),
  createError: null as Error | null,
  role: "user",
  primary: false,
}));
vi.mock("@/components/realtimeEventsContext", () => ({ useEventChannel: vi.fn() }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p", name: "Member", is_primary: state.primary } }),
}));
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { role: state.role } }) }));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p", name: "Member" }] }),
}));
vi.mock("@/hooks/queries/history-import", () => ({
  useHistoryImportSources: () => ({ data: [], isLoading: false }),
  useHistoryImportCapability: () => ({ data: undefined }),
  useHistoryImportRuns: () => ({
    data: [
      {
        id: "saved",
        status: "canceling",
        source_type: "plex",
        profile_id: "p",
        connection_mode: "plex_oauth",
        terminal: false,
        cancelable: false,
        created_at: "2026-09-01T00:00:00Z",
        fetched: 0,
        matched: 0,
        unmatched: 0,
        progress_updated: 0,
        history_created: 0,
        watchlist_added: 0,
        favorites_imported: 0,
        skipped: 0,
        warnings: [],
        unmatched_samples: [],
      },
    ],
  }),
  useHistoryImportRun: vi.fn(),
  useLoginEmbyConnect: () => ({ isPending: false, mutateAsync: state.login }),
  useCreateHistoryImportRun: () => ({
    isPending: false,
    mutateAsync: state.createRun,
    error: state.createError,
  }),
}));
const unavailableRun = () =>
  ({
    data: undefined,
    error: new Error("unavailable"),
    refetch: state.refresh,
  }) as unknown as ReturnType<typeof useHistoryImportRun>;
beforeEach(() => {
  vi.mocked(useHistoryImportRun).mockImplementation(unavailableRun);
  state.role = "user";
  state.primary = false;
  state.createError = null;
});
afterEach(cleanup);
it("monitors the latest persisted run and lets failed polling be refreshed", () => {
  render(
    <MemoryRouter>
      <HistoryImportSettings />
    </MemoryRouter>,
  );
  expect(useHistoryImportRun).toHaveBeenCalledWith("saved");
  expect(screen.getAllByText("Cancelling").length).toBeGreaterThan(0);
  expect(screen.getByRole("alert").textContent).toContain("Check its status");
  fireEvent.click(screen.getByRole("button", { name: "Refresh status" }));
  expect(state.refresh).toHaveBeenCalledTimes(1);
  expect(screen.queryByRole("button", { name: "Cancel import" })).toBeNull();
});

function renderPage() {
  render(
    <MemoryRouter>
      <HistoryImportSettings />
    </MemoryRouter>,
  );
}

it("asks for a fresh Emby Connect sign-in once a run consumes the session", async () => {
  state.login.mockResolvedValue({
    connect_session_id: "connect-1",
    servers: [{ server_id: "srv", name: "Home", has_remote_url: true, has_local_address: false }],
    expires_at: "2026-09-01T00:30:00Z",
  });
  state.createRun.mockResolvedValue({ id: "new-run" });
  renderPage();

  fireEvent.click(screen.getByRole("button", { name: "Find Servers" }));
  expect(await screen.findByText("Connected")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Start Import" }));

  await waitFor(() => expect(screen.queryByText("Connected")).toBeNull());
  expect(state.createRun).toHaveBeenCalledWith(
    expect.objectContaining({ source: "emby", connect_session_id: "connect-1", server_id: "srv" }),
  );
  expect(screen.getByRole("button", { name: "Find Servers" })).toBeTruthy();
});

it("counts skipped items once in a running import's progress", () => {
  vi.mocked(useHistoryImportRun).mockReturnValue({
    data: {
      id: "running",
      status: "running",
      source_type: "emby",
      connection_mode: "predefined",
      terminal: false,
      cancelable: false,
      created_at: "2026-09-01T00:00:00Z",
      fetched: 13,
      matched: 11,
      unmatched: 2,
      progress_updated: 0,
      history_created: 0,
      watchlist_added: 0,
      favorites_imported: 0,
      skipped: 8,
      warnings: [
        "An import item could not be processed.",
        "An import item could not be processed.",
      ],
      unmatched_samples: [],
    },
    error: null,
    refetch: state.refresh,
  } as unknown as ReturnType<typeof useHistoryImportRun>);
  renderPage();

  expect(screen.getByText("13 processed so far")).toBeTruthy();
  expect(screen.getAllByText("An import item could not be processed.")).toHaveLength(2);
});

it("lets the primary profile choose the profile and names each run's profile", () => {
  state.primary = true;
  renderPage();
  expect(screen.getByRole("combobox", { name: "Import into profile" })).toBeTruthy();
  expect(screen.getByText(/into Member/)).toBeTruthy();
});

it("imports only into its own profile from an admin's non-primary profile", () => {
  // The server refuses a non-primary profile's cross-profile import even on
  // an admin account, so the picker must not offer it.
  state.role = "admin";
  renderPage();
  expect(screen.queryByRole("combobox", { name: "Import into profile" })).toBeNull();
  expect(screen.getByText(/The history goes into your profile, Member\./)).toBeTruthy();
});

it("keeps the server's message for a secondary profile's refused import", () => {
  // A secondary profile only imports into itself, so a 403 is some other
  // refusal (demo mode here) and must not be reworded as a cross-profile one.
  state.createError = new V2ProblemError("createHistoryImportRun", {
    type: "https://silo.example/problems/forbidden",
    title: "Forbidden",
    status: 403,
    detail: "This action is not available in demo mode.",
    instance: "/api/v2/history-imports/runs",
  });
  renderPage();
  const alerts = screen.getAllByRole("alert").map((alert) => alert.textContent ?? "");
  expect(alerts.some((text) => text.includes("Ask the primary profile"))).toBe(false);
  expect(alerts.some((text) => text.includes(state.createError!.message))).toBe(true);
});

it("keeps the server's message for a refused import by the primary profile", () => {
  state.primary = true;
  state.createError = new V2ProblemError("createHistoryImportRun", {
    type: "https://silo.example/problems/forbidden",
    title: "Forbidden",
    status: 403,
    detail: "Verify the primary profile PIN to import into another profile",
    instance: "/api/v2/history-imports/runs",
  });
  renderPage();
  const alerts = screen.getAllByRole("alert").map((alert) => alert.textContent ?? "");
  expect(alerts.some((text) => text.includes("This profile can only import"))).toBe(false);
  expect(alerts.some((text) => text.includes(state.createError!.message))).toBe(true);
});

it("imports a Plex Home member with that member's own tokens", async () => {
  Object.defineProperties(Element.prototype, {
    hasPointerCapture: { configurable: true, value: () => false },
    setPointerCapture: { configurable: true, value: () => {} },
    releasePointerCapture: { configurable: true, value: () => {} },
    scrollIntoView: { configurable: true, value: () => {} },
  });
  const server = (accessToken: string) => ({
    name: "Living Room",
    clientIdentifier: "srv",
    provides: "server",
    owned: true,
    accessToken,
    connections: [{ uri: "https://srv.plex.direct:32400", local: false }],
  });
  const fetchMock = vi.fn(async (input: string, init?: RequestInit) => {
    const url = new URL(input);
    const token = new Headers(init?.headers).get("X-Plex-Token");
    switch (`${init?.method ?? "GET"} ${url.pathname}`) {
      case "GET /api/v2/pins/7":
        return Response.json({ authToken: "owner-token" });
      case "GET /api/v2/resources":
        return Response.json([server(token === "kid-token" ? "kid-server" : "owner-server")]);
      case "GET /api/v2/user":
        return Response.json({ uuid: "owner" });
      case "GET /api/v2/home/users":
        return Response.json({
          users: [
            { uuid: "owner", title: "Owner" },
            { uuid: "kid", title: "Kid", protected: true, restricted: true },
          ],
        });
      case "POST /api/v2/home/users/kid/switch":
        return token === "owner-token" && url.searchParams.get("pin") === "1234"
          ? Response.json({ authToken: "kid-token" })
          : Response.json({}, { status: 403 });
    }
    return new Response("not found", { status: 404 });
  });
  vi.stubGlobal("fetch", fetchMock);
  state.createRun.mockResolvedValue({ id: "new-run" });
  try {
    render(
      <MemoryRouter
        initialEntries={["/settings/history-import?plex_auth=1&plex_pin_id=7&plex_pin_code=c"]}
      >
        <HistoryImportSettings />
      </MemoryRouter>,
    );

    fireEvent.click(await screen.findByRole("combobox", { name: "Plex Home user" }));
    fireEvent.click(await screen.findByRole("option", { name: "Kid (managed)" }));
    const start = screen.getByRole("button", { name: "Start Import" });
    // A protected member's PIN is required before the switch is attempted.
    expect((start as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Kid's Plex Home PIN"), { target: { value: "1234" } });
    fireEvent.click(start);

    await waitFor(() => expect(state.createRun).toHaveBeenCalled());
    expect(state.createRun).toHaveBeenCalledWith(
      expect.objectContaining({
        source: "plex",
        plex_base_url: "https://srv.plex.direct:32400",
        plex_token: "kid-server",
        plex_account_token: "kid-token",
      }),
    );
  } finally {
    vi.unstubAllGlobals();
  }
});

it("finishes Plex sign-in while the optional Plex Home lookup hangs", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: string) => {
      const path = new URL(input).pathname;
      if (path === "/api/v2/pins/7") return Promise.resolve(Response.json({ authToken: "owner" }));
      if (path === "/api/v2/resources") {
        return Promise.resolve(
          Response.json([
            {
              name: "Living Room",
              clientIdentifier: "srv",
              provides: "server",
              owned: true,
              accessToken: "owner-server",
              connections: [{ uri: "https://srv.plex.direct:32400", local: false }],
            },
          ]),
        );
      }
      // plex.tv never answers the Home lookups.
      return new Promise<Response>(() => {});
    }),
  );
  try {
    render(
      <MemoryRouter
        initialEntries={["/settings/history-import?plex_auth=1&plex_pin_id=7&plex_pin_code=c"]}
      >
        <HistoryImportSettings />
      </MemoryRouter>,
    );

    expect(await screen.findByText("Signed in")).toBeTruthy();
    expect(screen.queryByRole("combobox", { name: "Plex Home user" })).toBeNull();
  } finally {
    vi.unstubAllGlobals();
  }
});
