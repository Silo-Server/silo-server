// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import HistoryImportSettings from "./HistoryImportSettings";
import { useHistoryImportRun } from "@/hooks/queries/history-import";
const state = vi.hoisted(() => ({ refresh: vi.fn(), login: vi.fn(), createRun: vi.fn() }));
vi.mock("@/components/realtimeEventsContext", () => ({ useEventChannel: vi.fn() }));
vi.mock("@/hooks/useCurrentProfile", () => ({
  useCurrentProfile: () => ({ profile: { id: "p" } }),
}));
vi.mock("@/hooks/queries/profiles", () => ({
  useProfiles: () => ({ data: [{ id: "p", name: "Member" }] }),
}));
vi.mock("@/hooks/queries/history-import", () => ({
  useHistoryImportSources: () => ({ data: [], isLoading: false }),
  useHistoryImportRuns: () => ({
    data: [
      {
        id: "saved",
        status: "canceling",
        source_type: "plex",
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
  useCreateHistoryImportRun: () => ({ isPending: false, mutateAsync: state.createRun }),
}));
const unavailableRun = () =>
  ({
    data: undefined,
    error: new Error("unavailable"),
    refetch: state.refresh,
  }) as unknown as ReturnType<typeof useHistoryImportRun>;
beforeEach(() => {
  vi.mocked(useHistoryImportRun).mockImplementation(unavailableRun);
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

  expect(screen.getByText("13 / 13 processed")).toBeTruthy();
  expect(screen.getAllByText("An import item could not be processed.")).toHaveLength(2);
});
