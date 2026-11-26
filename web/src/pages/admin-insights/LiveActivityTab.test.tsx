import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminSession } from "@/api/types";
import { LiveActivityTab } from "./LiveActivityTab";

interface SessionsState {
  data: AdminSession[] | undefined;
  isLoading: boolean;
  error: Error | null;
}

const state = vi.hoisted(() => ({
  sessions: { data: undefined, isLoading: false, error: null } as SessionsState,
  refetch: vi.fn(async () => ({})),
}));

vi.mock("@/hooks/queries/admin/stats", () => ({
  useAdminSessions: () => ({ ...state.sessions, refetch: state.refetch }),
}));
vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ connectionState: "live" }),
}));
vi.mock("@/hooks/usePageActivity", () => ({
  usePageActivity: () => ({ canApplyRealtimeUpdates: true }),
}));
vi.mock("@/hooks/queries/admin/logs", () => ({
  useOperationalLogs: () => ({ data: undefined, isLoading: false, isFetching: false }),
}));
vi.mock("@/hooks/queries/admin/ips", () => ({
  useIPUsers: () => ({ data: [], isLoading: false, restart: vi.fn() }),
}));
vi.mock("@/components/AdminSessionActions", () => ({ AdminSessionActions: () => null }));

function makeSession(id: string, minute = 0): AdminSession {
  return {
    session_id: id,
    user_id: 1,
    username: `viewer-${id}`,
    profile_id: "p1",
    media_file_id: 42,
    requested_media_file_id: 42,
    media_title: `Title ${id}`,
    media_type: "movie",
    play_method: "DirectPlay",
    started_at: `2026-11-25T12:${String(minute).padStart(2, "0")}:00Z`,
  } as AdminSession;
}

function sessions(count: number): AdminSession[] {
  return Array.from({ length: count }, (_, i) => makeSession(`s${i + 1}`, i));
}

const queryClient = new QueryClient();
function ui() {
  return (
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <LiveActivityTab />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function pressedLayout(): string | undefined {
  return screen
    .getAllByRole("button", { pressed: true })
    .map((button) => button.textContent ?? "")
    .find((text) => text === "Table" || text === "Cards");
}

beforeEach(() => {
  localStorage.clear();
  state.sessions = { data: undefined, isLoading: false, error: null };
  state.refetch.mockClear();
});
afterEach(() => {
  cleanup();
});

describe("LiveActivityTab", () => {
  it("says the list failed to load instead of showing no streams", () => {
    state.sessions = { data: undefined, isLoading: false, error: new Error("boom") };
    render(ui());
    expect(screen.queryByText(/no active streams/i)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /retry/i }));
    expect(state.refetch).toHaveBeenCalledTimes(1);
  });

  it("keeps the last list when a later refresh fails", () => {
    state.sessions = { data: sessions(2), isLoading: false, error: new Error("boom") };
    render(ui());
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
    expect(screen.getByText("Title s1")).toBeTruthy();
  });

  it("picks the layout from the first loaded list, not from a load that hadn't finished", () => {
    const view = render(ui());
    state.sessions = { data: sessions(10), isLoading: false, error: null };
    view.rerender(ui());
    expect(pressedLayout()).toBe("Table");

    // Chosen once: fewer streams later don't switch it back to cards.
    state.sessions = { data: sessions(2), isLoading: false, error: null };
    view.rerender(ui());
    expect(pressedLayout()).toBe("Table");
  });

  it("doesn't reopen a closed panel when its session shows up again", () => {
    state.sessions = { data: sessions(2), isLoading: false, error: null };
    const view = render(ui());
    expect(pressedLayout()).toBe("Cards");
    fireEvent.click(screen.getAllByRole("button", { name: /details/i })[0]!);
    expect(screen.getByRole("dialog")).toBeTruthy();

    // The newest stream, listed first, drops out, then a later read lists it again.
    state.sessions = { data: [makeSession("s1", 0)], isLoading: false, error: null };
    view.rerender(ui());
    state.sessions = { data: sessions(2), isLoading: false, error: null };
    view.rerender(ui());
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
