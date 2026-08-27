import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { invalidateCatalogState } from "./realtimeCatalogInvalidation";
import { RealtimeEventsProvider } from "./RealtimeEventsProvider";

vi.mock("./realtimeCatalogInvalidation", () => ({
  invalidateCatalogState: vi.fn(),
}));

vi.mock("@/hooks/useAuth", () => {
  const useAuth = () => ({
    user: {
      id: 1,
      username: "admin",
      email: "admin@example.com",
      role: "admin",
      permissions: [],
      download_allowed: true,
    },
    profile: null,
  });
  return { useAuth, useOptionalAuth: useAuth };
});

const mockPageActivity = vi.hoisted(() => ({
  isVisible: true,
  isFocused: true,
  isFrozen: false,
  canPollDashboard: true,
  canApplyRealtimeUpdates: true,
}));

vi.mock("@/hooks/usePageActivity", () => ({
  usePageActivity: () => ({ ...mockPageActivity }),
}));

vi.mock("react-router", () => ({
  useLocation: () => ({ pathname: "/" }),
}));

class FakeWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  readyState = FakeWebSocket.CONNECTING;

  constructor(public url: string) {
    FakeWebSocket.instances.push(this);
  }

  send() {}

  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }
}

function renderProvider() {
  const queryClient = new QueryClient();
  const view = render(
    <QueryClientProvider client={queryClient}>
      <RealtimeEventsProvider>
        <div />
      </RealtimeEventsProvider>
    </QueryClientProvider>,
  );
  const rerender = () =>
    view.rerender(
      <QueryClientProvider client={queryClient}>
        <RealtimeEventsProvider>
          <div />
        </RealtimeEventsProvider>
      </QueryClientProvider>,
    );
  return { view, rerender, socket: FakeWebSocket.instances[0] };
}

function emitCatalogEvent(socket: FakeWebSocket | undefined, event: string, data: object) {
  act(() => {
    socket?.onmessage?.({
      data: JSON.stringify({ type: "event", channel: "catalog", event, data }),
    } as MessageEvent);
  });
}

describe("RealtimeEventsProvider broad catalog throttle", () => {
  beforeEach(() => {
    FakeWebSocket.instances = [];
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", FakeWebSocket);
    vi.mocked(invalidateCatalogState).mockClear();
    mockPageActivity.canApplyRealtimeUpdates = true;
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("coalesces a catalog event burst into one leading and one trailing pass", () => {
    const { socket } = renderProvider();

    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "a", library_id: 3 });
    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "b", library_id: 3 });
    emitCatalogEvent(socket, "catalog.library.changed", { library_id: 5 });

    expect(invalidateCatalogState).toHaveBeenCalledTimes(1);
    expect(vi.mocked(invalidateCatalogState).mock.calls[0]?.[1]).toMatchObject({
      libraryId: 3,
      includeLibraryLists: false,
    });

    act(() => {
      vi.advanceTimersByTime(15_000);
    });

    expect(invalidateCatalogState).toHaveBeenCalledTimes(2);
    const trailing = vi.mocked(invalidateCatalogState).mock.calls[1]?.[1];
    // Coalesced options: differing library ids collapse to all libraries and
    // includeLibraryLists ORs to true.
    expect(trailing?.libraryId).toBeUndefined();
    expect(trailing?.includeLibraryLists).toBe(true);

    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(2);
  });

  it("fires a fresh leading pass once the window after the trailing pass closes", () => {
    const { socket } = renderProvider();

    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "a", library_id: 3 });
    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "b", library_id: 3 });
    act(() => {
      vi.advanceTimersByTime(15_000);
    });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(2);

    // A wedged throttle (timer never cleared, lastRunAt never reset) would
    // swallow every later broad pass; a fresh event after the window must
    // fire the leading edge again.
    act(() => {
      vi.advanceTimersByTime(15_000);
    });
    emitCatalogEvent(socket, "catalog.library.changed", { library_id: 5 });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(3);
  });

  it("skips the trailing pass while realtime updates are suppressed", () => {
    const { rerender, socket } = renderProvider();

    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "a", library_id: 3 });
    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "b", library_id: 3 });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(1);

    // Hide the tab before the trailing timer fires: the pass is dropped like
    // any other realtime update (the catch-up-on-focus refetch covers it).
    act(() => {
      mockPageActivity.canApplyRealtimeUpdates = false;
      rerender();
    });
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(1);
  });

  it("resets the throttle when a job-terminal pass runs", () => {
    const { socket } = renderProvider();

    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "a", library_id: 3 });
    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "b", library_id: 3 });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(1);

    // catalog_import completion sweeps everything immediately and must
    // swallow the pending trailing pass instead of running a second full
    // sweep moments later.
    act(() => {
      socket?.onmessage?.({
        data: JSON.stringify({
          type: "event",
          channel: "jobs",
          event: "job.completed",
          data: {
            id: "job-1",
            job_type: "catalog_import",
            status: "completed",
            requested_at: "2026-08-27T00:00:00Z",
          },
        }),
      } as MessageEvent);
    });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(2);

    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(2);
  });

  it("drops the pending trailing pass on unmount", () => {
    const { view, socket } = renderProvider();

    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "a", library_id: 3 });
    emitCatalogEvent(socket, "catalog.item.changed", { content_id: "b", library_id: 3 });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(1);

    view.unmount();
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(invalidateCatalogState).toHaveBeenCalledTimes(1);
  });
});
