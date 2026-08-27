import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  adminKeys,
  catalogKeys,
  favoriteKeys,
  historyKeys,
  itemKeys,
  libraryKeys,
  progressKeys,
  sectionKeys,
  watchlistKeys,
} from "@/hooks/queries/keys";
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

  emitClose() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.();
  }
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

function renderProvider(queryClient: QueryClient) {
  return render(
    <QueryClientProvider client={queryClient}>
      <RealtimeEventsProvider>
        <div />
      </RealtimeEventsProvider>
    </QueryClientProvider>,
  );
}

function emitSocketMessage(socket: FakeWebSocket | undefined, message: object) {
  act(() => {
    socket?.onmessage?.({ data: JSON.stringify(message) } as MessageEvent);
  });
}

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

  it("invalidates only the changed item's queries for per-item catalog events", () => {
    const queryClient = new QueryClient();
    renderProvider(queryClient);
    const socket = FakeWebSocket.instances[0];

    // The first event spends the throttle's leading edge on a broad pass.
    emitSocketMessage(socket, {
      type: "event",
      channel: "catalog",
      event: "catalog.item.changed",
      data: { content_id: "prime", library_id: 3 },
    });

    queryClient.setQueryData(catalogKeys.itemDetail("target"), { content_id: "target" });
    queryClient.setQueryData(itemKeys.watchDetail("target"), { content_id: "target" });
    queryClient.setQueryData(favoriteKeys.check("target"), { is_favorite: false });
    queryClient.setQueryData(watchlistKeys.check("target"), { in_watchlist: false });
    queryClient.setQueryData(catalogKeys.itemDetail("other"), { content_id: "other" });
    queryClient.setQueryData(catalogKeys.seriesSeasons("other"), { seasons: [] });

    emitSocketMessage(socket, {
      type: "event",
      channel: "catalog",
      event: "catalog.item.changed",
      data: { content_id: "target", library_id: 3 },
    });

    expect(queryClient.getQueryState(catalogKeys.itemDetail("target"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(itemKeys.watchDetail("target"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(favoriteKeys.check("target"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(watchlistKeys.check("target"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(catalogKeys.itemDetail("other"))?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(catalogKeys.seriesSeasons("other"))?.isInvalidated).toBe(
      false,
    );

    // The trailing broad pass still refreshes everything once per window.
    act(() => {
      vi.advanceTimersByTime(15_000);
    });
    expect(queryClient.getQueryState(catalogKeys.itemDetail("other"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(catalogKeys.seriesSeasons("other"))?.isInvalidated).toBe(true);
  });

  it("keeps catalog surfaces untouched for user_state progress events", () => {
    const queryClient = new QueryClient();
    renderProvider(queryClient);
    const socket = FakeWebSocket.instances[0];

    const browseKey = itemKeys.browse({
      q: "",
      type: "all",
      sort: "created_at",
      order: "desc",
      offset: 0,
      limit: 20,
    });
    const catalogListKey = catalogKeys.list({ source: "query", q: "star", limit: 20, offset: 0 });
    queryClient.setQueryData(browseKey, { items: [] });
    queryClient.setQueryData(catalogListKey, { items: [] });
    queryClient.setQueryData(progressKeys.list(), { progress: [] });
    queryClient.setQueryData(historyKeys.list(), { items: [] });
    queryClient.setQueryData(sectionKeys.homeItems("continue"), { section: null });
    queryClient.setQueryData(favoriteKeys.list(), { items: [] });
    queryClient.setQueryData(catalogKeys.itemDetail("target"), { content_id: "target" });
    queryClient.setQueryData(catalogKeys.seriesSeasons("show"), { seasons: [] });
    queryClient.setQueryData(catalogKeys.seasonEpisodes("show", 1), { episodes: [] });
    queryClient.setQueryData(catalogKeys.itemEpisodes("show"), { episodes: [] });
    queryClient.setQueryData(catalogKeys.seriesSeasons("other-show"), { seasons: [] });

    emitSocketMessage(socket, {
      type: "event",
      channel: "user_state",
      event: "user_state.changed",
      data: {
        profile_id: "profile-1",
        content_id: "target",
        series_id: "show",
        change: "progress",
      },
    });

    expect(queryClient.getQueryState(progressKeys.list())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(historyKeys.list())?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(sectionKeys.homeItems("continue"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(catalogKeys.itemDetail("target"))?.isInvalidated).toBe(true);
    // The episode's series refreshes immediately so an open series page's
    // watched ticks and season counters track playback on another device.
    expect(queryClient.getQueryState(catalogKeys.seriesSeasons("show"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(catalogKeys.seasonEpisodes("show", 1))?.isInvalidated).toBe(
      true,
    );
    expect(queryClient.getQueryState(catalogKeys.itemEpisodes("show"))?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(catalogKeys.seriesSeasons("other-show"))?.isInvalidated).toBe(
      false,
    );
    expect(queryClient.getQueryState(browseKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(catalogListKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(favoriteKeys.list())?.isInvalidated).toBe(false);

    // The remaining watch-state surfaces (browse grids, catalog lists) catch
    // up through the trailing-only broad throttle instead of staying stale
    // forever — threshold completion emits only change:"progress".
    act(() => {
      vi.advanceTimersByTime(15_000);
    });
    expect(queryClient.getQueryState(browseKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(catalogListKey)?.isInvalidated).toBe(true);
  });
});
