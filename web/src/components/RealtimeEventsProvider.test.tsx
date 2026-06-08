import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { adminKeys, catalogKeys, libraryKeys, sectionKeys } from "@/hooks/queries/keys";
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

vi.mock("@/hooks/useAuth", () => ({
  useAuth: () => ({
    user: mockState.user,
    profile: null,
  }),
}));

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
});
