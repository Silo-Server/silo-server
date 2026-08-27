import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { adminKeys, catalogKeys, libraryKeys, sectionKeys } from "@/hooks/queries/keys";
import type { ItemDetail } from "@/api/types";
import { invalidateCatalogState } from "./realtimeCatalogInvalidation";
import { buildEventsUrl, RealtimeEventsProvider } from "./RealtimeEventsProvider";

const mockState = vi.hoisted(() => ({
  user: {
    id: 1,
    username: "admin",
    email: "admin@example.com",
    role: "admin",
    permissions: [],
    download_allowed: true,
  },
  pageActivity: {
    isVisible: true,
    isFocused: true,
    isFrozen: false,
    canPollDashboard: true,
    canApplyRealtimeUpdates: true,
  },
  pathname: "/",
}));

vi.mock("@/hooks/useAuth", () => {
  const useAuth = () => ({
    user: mockState.user,
    profile: null,
  });
  return { useAuth, useOptionalAuth: useAuth };
});

vi.mock("@/hooks/usePageActivity", () => ({
  usePageActivity: () => mockState.pageActivity,
}));

vi.mock("react-router", () => ({
  useLocation: () => ({ pathname: mockState.pathname }),
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

  emitClose() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.();
  }

  emitMessage(message: unknown) {
    this.onmessage?.({ data: JSON.stringify(message) } as MessageEvent);
  }
}

function renderProvider(queryClient: QueryClient) {
  // Live-session invalidation is gated on the admin dashboard route
  // (isDashboardRoute), and the suite's beforeEach parks the router on "/".
  mockState.pathname = "/admin";
  return render(
    <QueryClientProvider client={queryClient}>
      <RealtimeEventsProvider>
        <div />
      </RealtimeEventsProvider>
    </QueryClientProvider>,
  );
}

function emitSessionsReplaced(socket: FakeWebSocket) {
  socket.emitMessage({
    type: "event",
    channel: "sessions",
    event: "sessions.replaced",
    data: [],
  });
}

function liveInvalidationCalls(spy: ReturnType<typeof vi.spyOn>) {
  return spy.mock.calls.filter(
    ([filters]) =>
      JSON.stringify(filters?.queryKey) === JSON.stringify(adminKeys.liveSessionsRoot()),
  );
}

describe("buildEventsUrl", () => {
  it("includes auth token and websocket scheme", () => {
    expect(
      buildEventsUrl("token-123", {
        protocol: "https:",
        host: "example.com",
      }),
    ).toBe("wss://example.com/api/v1/events/ws?token=token-123");
  });

  it("omits the query string when no token is available", () => {
    expect(
      buildEventsUrl(null, {
        protocol: "http:",
        host: "localhost:5173",
      }),
    ).toBe("ws://localhost:5173/api/v1/events/ws");
  });
});

describe("invalidateCatalogState", () => {
  it("invalidates library lists for a scoped library change", async () => {
    const queryClient = new QueryClient();
    const otherCatalogKey = catalogKeys.list({
      source: "section",
      scope: "library",
      section_id: "all",
      library_id: 1,
      limit: 60,
      offset: 0,
    });
    const changedCatalogKey = catalogKeys.list({
      source: "section",
      scope: "library",
      section_id: "all",
      library_id: 3,
      limit: 60,
      offset: 0,
    });
    const otherSectionKey = sectionKeys.libraryLayout(1);
    const changedSectionKey = sectionKeys.libraryLayout(3);
    const userLibrariesKey = libraryKeys.user("profile-1");

    queryClient.setQueryData(adminKeys.libraries(), []);
    queryClient.setQueryData(adminKeys.libraryMatchQueueStatuses(), []);
    queryClient.setQueryData(userLibrariesKey, []);
    queryClient.setQueryData(otherCatalogKey, { items: [] });
    queryClient.setQueryData(changedCatalogKey, { items: [] });
    queryClient.setQueryData(otherSectionKey, { sections: [] });
    queryClient.setQueryData(changedSectionKey, { sections: [] });

    invalidateCatalogState(queryClient, { libraryId: 3, allowDashboardRefetch: false });
    await Promise.resolve();

    expect(queryClient.getQueryState(adminKeys.libraries())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(adminKeys.libraryMatchQueueStatuses())?.isInvalidated).toBe(
      true,
    );
    expect(queryClient.getQueryState(userLibrariesKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(otherCatalogKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(changedCatalogKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(otherSectionKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(changedSectionKey)?.isInvalidated).toBe(true);
  });

  it("can skip library lists for item-scoped catalog changes", async () => {
    const queryClient = new QueryClient();
    const changedCatalogKey = catalogKeys.list({
      source: "section",
      scope: "library",
      section_id: "all",
      library_id: 3,
      limit: 60,
      offset: 0,
    });

    queryClient.setQueryData(adminKeys.libraries(), []);
    queryClient.setQueryData(adminKeys.libraryMatchQueueStatuses(), []);
    queryClient.setQueryData(libraryKeys.all, []);
    queryClient.setQueryData(changedCatalogKey, { items: [] });

    invalidateCatalogState(queryClient, {
      itemId: "item-1",
      libraryId: 3,
      allowDashboardRefetch: false,
      includeLibraryLists: false,
    });
    await Promise.resolve();

    expect(queryClient.getQueryState(adminKeys.libraries())?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(adminKeys.libraryMatchQueueStatuses())?.isInvalidated).toBe(
      false,
    );
    expect(queryClient.getQueryState(libraryKeys.all)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(changedCatalogKey)?.isInvalidated).toBe(true);
  });
});

describe("RealtimeEventsProvider", () => {
  beforeEach(() => {
    FakeWebSocket.instances = [];
    vi.useFakeTimers();
    vi.stubGlobal("WebSocket", FakeWebSocket);
    mockState.pageActivity = {
      isVisible: true,
      isFocused: true,
      isFrozen: false,
      canPollDashboard: true,
      canApplyRealtimeUpdates: true,
    };
    mockState.pathname = "/";
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("ignores stale close events from intentionally closed sockets", () => {
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    });
    const view = render(
      <QueryClientProvider client={queryClient}>
        <RealtimeEventsProvider>
          <div />
        </RealtimeEventsProvider>
      </QueryClientProvider>,
    );

    expect(FakeWebSocket.instances).toHaveLength(1);
    const firstSocket = FakeWebSocket.instances[0];

    act(() => {
      mockState.pageActivity = {
        ...mockState.pageActivity,
        canApplyRealtimeUpdates: false,
      };
      view.rerender(
        <QueryClientProvider client={queryClient}>
          <RealtimeEventsProvider>
            <div />
          </RealtimeEventsProvider>
        </QueryClientProvider>,
      );
    });

    act(() => {
      mockState.pageActivity = {
        ...mockState.pageActivity,
        canApplyRealtimeUpdates: true,
      };
      view.rerender(
        <QueryClientProvider client={queryClient}>
          <RealtimeEventsProvider>
            <div />
          </RealtimeEventsProvider>
        </QueryClientProvider>,
      );
    });

    expect(FakeWebSocket.instances).toHaveLength(2);

    act(() => {
      firstSocket?.emitClose();
      vi.advanceTimersByTime(1_000);
    });

    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("defers broad catch-up refetches until foreground playback exits", () => {
    const queryClient = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    });
    const refetchQueries = vi.spyOn(queryClient, "refetchQueries").mockResolvedValue(undefined);
    mockState.pathname = "/watch/movie-1";
    const provider = () => (
      <QueryClientProvider client={queryClient}>
        <RealtimeEventsProvider>
          <div />
        </RealtimeEventsProvider>
      </QueryClientProvider>
    );

    const view = render(provider());

    act(() => {
      mockState.pageActivity = {
        ...mockState.pageActivity,
        isVisible: false,
        canApplyRealtimeUpdates: false,
      };
      view.rerender(provider());
    });

    act(() => {
      mockState.pageActivity = {
        ...mockState.pageActivity,
        isVisible: true,
        canApplyRealtimeUpdates: true,
      };
      view.rerender(provider());
    });

    expect(refetchQueries).not.toHaveBeenCalled();

    act(() => {
      mockState.pathname = "/item/movie-1";
      view.rerender(provider());
    });

    expect(refetchQueries).toHaveBeenCalledTimes(1);
    expect(refetchQueries).toHaveBeenCalledWith({
      type: "active",
      predicate: expect.any(Function),
    });
  });

  it("preserves cached watched state when a favorite-only event arrives", () => {
    const queryClient = new QueryClient();
    const detailKey = catalogKeys.itemDetail("movie-1");
    queryClient.setQueryData<ItemDetail>(detailKey, {
      content_id: "movie-1",
      type: "movie",
      user_data: { played: true },
    } as ItemDetail);

    render(
      <QueryClientProvider client={queryClient}>
        <RealtimeEventsProvider>
          <div />
        </RealtimeEventsProvider>
      </QueryClientProvider>,
    );

    act(() => {
      FakeWebSocket.instances[0]?.emitMessage({
        type: "event",
        channel: "user_state",
        event: "favorite.updated",
        data: {
          profile_id: "profile-1",
          content_id: "movie-1",
          change: "favorite",
          is_favorite: true,
        },
      });
    });

    expect(queryClient.getQueryData<ItemDetail>(detailKey)).toMatchObject({
      user_data: { played: true },
      user_state: { played: true, is_favorite: true },
    });
  });

  it("bounds a burst to one leading and one trailing live-session invalidation", () => {
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
    renderProvider(queryClient);
    const socket = FakeWebSocket.instances[0];

    act(() => {
      for (let index = 0; index < 20; index += 1) emitSessionsReplaced(socket);
    });
    expect(liveInvalidationCalls(invalidate)).toHaveLength(1);

    act(() => vi.advanceTimersByTime(5_000));
    expect(liveInvalidationCalls(invalidate)).toHaveLength(1);

    act(() => vi.advanceTimersByTime(5_001));
    expect(liveInvalidationCalls(invalidate)).toHaveLength(2);
  });

  it("invalidates synchronously on the first live-session event", () => {
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
    renderProvider(queryClient);

    act(() => emitSessionsReplaced(FakeWebSocket.instances[0]));

    expect(liveInvalidationCalls(invalidate)).toHaveLength(1);
  });

  it("runs another leading invalidation at the exact throttle boundary", () => {
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
    renderProvider(queryClient);
    const socket = FakeWebSocket.instances[0];

    act(() => emitSessionsReplaced(socket));
    act(() => vi.advanceTimersByTime(10_000));
    act(() => emitSessionsReplaced(socket));

    expect(liveInvalidationCalls(invalidate)).toHaveLength(2);
  });

  it("bounds a fresh burst after a trailing invalidation", () => {
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
    renderProvider(queryClient);
    const socket = FakeWebSocket.instances[0];

    act(() => {
      emitSessionsReplaced(socket);
      emitSessionsReplaced(socket);
      vi.advanceTimersByTime(10_000);
    });
    expect(liveInvalidationCalls(invalidate)).toHaveLength(2);

    act(() => {
      for (let index = 0; index < 10; index += 1) emitSessionsReplaced(socket);
      vi.advanceTimersByTime(9_999);
    });
    expect(liveInvalidationCalls(invalidate)).toHaveLength(2);

    act(() => vi.advanceTimersByTime(1));
    expect(liveInvalidationCalls(invalidate)).toHaveLength(3);
  });

  it("tracks invalidation windows independently for each query client", () => {
    const firstClient = new QueryClient();
    const secondClient = new QueryClient();
    const firstInvalidate = vi.spyOn(firstClient, "invalidateQueries").mockResolvedValue();
    const secondInvalidate = vi.spyOn(secondClient, "invalidateQueries").mockResolvedValue();
    renderProvider(firstClient);
    renderProvider(secondClient);

    act(() => {
      emitSessionsReplaced(FakeWebSocket.instances[0]);
      emitSessionsReplaced(FakeWebSocket.instances[1]);
    });

    expect(liveInvalidationCalls(firstInvalidate)).toHaveLength(1);
    expect(liveInvalidationCalls(secondInvalidate)).toHaveLength(1);
  });

  it("marks a trailing invalidation stale without refetching after the page becomes inactive", () => {
    const queryClient = new QueryClient();
    const invalidate = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
    const view = renderProvider(queryClient);
    const socket = FakeWebSocket.instances[0];

    act(() => {
      emitSessionsReplaced(socket);
      emitSessionsReplaced(socket);
    });
    act(() => {
      mockState.pageActivity = {
        ...mockState.pageActivity,
        canPollDashboard: false,
      };
      view.rerender(
        <QueryClientProvider client={queryClient}>
          <RealtimeEventsProvider>
            <div />
          </RealtimeEventsProvider>
        </QueryClientProvider>,
      );
    });
    act(() => vi.advanceTimersByTime(10_000));

    expect(liveInvalidationCalls(invalidate)).toHaveLength(2);
    expect(liveInvalidationCalls(invalidate)[1]?.[0]).toMatchObject({ refetchType: "none" });
  });
});
