import { adminSessionsKey } from "@/api/v2/adminSessionsCache";
import {
  captureProfileRequestContext,
  setAccessToken,
  setProfileId,
  setProfileToken,
} from "@/api/client";
import type { ReactNode } from "react";
import { useRealtimeEvents } from "./realtimeEventsContext";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, cleanup, render, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { adminKeys, catalogKeys, libraryKeys, sectionKeys } from "@/hooks/queries/keys";
import type { ItemDetail, TaskInfo } from "@/api/types";
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
  profile: null as { id: string; has_pin: boolean } | null,
  pathname: "/",
}));

vi.mock("@/hooks/useAuth", () => {
  const useAuth = () => ({
    user: mockState.user,
    profile: mockState.profile,
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

  constructor(
    public url: string,
    public protocols?: string[],
  ) {
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

describe("buildEventsUrl", () => {
  it("uses the websocket scheme without URL credentials", () => {
    expect(
      buildEventsUrl({
        protocol: "https:",
        host: "example.com",
      }),
    ).toBe("wss://example.com/api/v2/events/ws");
  });

  it("omits the query string when no token is available", () => {
    expect(
      buildEventsUrl({
        protocol: "http:",
        host: "localhost:5173",
      }),
    ).toBe("ws://localhost:5173/api/v2/events/ws");
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
    setAccessToken("session-access");
    setProfileId(null);
    setProfileToken(null);
    mockState.profile = null;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(
        async () =>
          new Response(
            JSON.stringify({
              ticket: "a".repeat(43),
              protocol: "silo.events.v2",
              expires_in: 30,
              max_connection_seconds: 300,
            }),
            { headers: { "Content-Type": "application/json" } },
          ),
      ),
    );
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

  it("skips paginated job query state when looking for a cached terminal event", async () => {
    const client = new QueryClient();
    client.setQueryData([...adminKeys.jobs("__all"), "pages", 20], {
      pages: [{ items: [] }],
      pageParams: [undefined],
    });
    client.setQueryData(adminKeys.jobs("__all"), [{ id: "done", status: "completed" }]);
    const { result } = renderHook(() => useRealtimeEvents(), {
      wrapper: ({ children }: { children: ReactNode }) => (
        <QueryClientProvider client={client}>
          <RealtimeEventsProvider>{children}</RealtimeEventsProvider>
        </QueryClientProvider>
      ),
    });
    await expect(result.current.awaitAdminJob("done")).resolves.toMatchObject({
      id: "done",
      status: "completed",
    });
  });

  it("ignores stale close events from intentionally closed sockets", async () => {
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

    await act(async () => {});
    expect(FakeWebSocket.instances).toHaveLength(1);
    expect(FakeWebSocket.instances[0]?.protocols).toEqual([
      "silo.events.v2",
      `silo.ticket.${"a".repeat(43)}`,
    ]);
    const firstSocket = FakeWebSocket.instances[0];

    await act(async () => {
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

    await act(async () => {
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

    await act(async () => {
      firstSocket?.emitClose();
      vi.advanceTimersByTime(1_000);
    });

    expect(FakeWebSocket.instances).toHaveLength(2);
  });

  it("reconnects on same-profile PIN replacement and rejects old socket frames", async () => {
    setProfileId("profile-1");
    mockState.profile = { id: "profile-1", has_pin: false };
    const queryClient = new QueryClient();
    const detailKey = catalogKeys.itemDetail("movie-1");
    queryClient.setQueryData(detailKey, { content_id: "movie-1", type: "movie" });
    const provider = () => (
      <QueryClientProvider client={queryClient}>
        <RealtimeEventsProvider>
          <div />
        </RealtimeEventsProvider>
      </QueryClientProvider>
    );
    const view = render(provider());
    await act(async () => {});
    expect(FakeWebSocket.instances).toHaveLength(1);
    const oldSocket = FakeWebSocket.instances[0]!;
    // Preserve a queued callback even after cleanup removes the socket handler.
    const oldMessage = oldSocket.onmessage!;
    await act(async () => {
      setProfileToken("replacement-pin-proof");
      mockState.profile = { id: "profile-1", has_pin: true };
      view.rerender(provider());
    });
    expect(oldSocket.readyState).toBe(FakeWebSocket.CLOSED);
    expect(FakeWebSocket.instances).toHaveLength(2);
    const mintCalls = vi
      .mocked(fetch)
      .mock.calls.filter(([url]) => String(url).endsWith("/api/v2/events/ws-ticket"));
    expect(mintCalls).toHaveLength(2);
    expect(new Headers(mintCalls[1]![1]?.headers).get("X-Profile-Token")).toBe(
      "replacement-pin-proof",
    );
    const event = {
      type: "event",
      channel: "user_state",
      event: "favorite.updated",
      data: {
        profile_id: "profile-1",
        content_id: "movie-1",
        change: "favorite",
        is_favorite: true,
      },
    };
    await act(async () => {
      oldMessage({ data: JSON.stringify(event) } as MessageEvent);
      oldSocket.emitClose();
      vi.advanceTimersByTime(1_000);
    });
    expect(queryClient.getQueryData(detailKey)).not.toHaveProperty("user_state");
    expect(FakeWebSocket.instances).toHaveLength(2);
    await act(async () => {
      FakeWebSocket.instances[1]!.emitMessage(event);
    });
    expect(queryClient.getQueryData(detailKey)).toMatchObject({
      user_state: { is_favorite: true },
    });
    view.unmount();
    setProfileId(null);
    setProfileToken(null);
  });

  it.each(["snapshot", "event"])(
    "keeps the complete 205-row cache while a capped 200-row %s triggers scoped refetch",
    async (type) => {
      setProfileId("primary");
      mockState.profile = { id: "primary", has_pin: false };
      const authority = captureProfileRequestContext()!;
      const currentKey = adminSessionsKey(authority);
      const otherKey = adminSessionsKey({ ...authority, profileId: "other" });
      const queryClient = new QueryClient();
      const complete = Array.from({ length: 205 }, (_, i) => ({ id: String(i) }));
      for (const key of [currentKey, otherKey, adminKeys.sessions()]) {
        queryClient.setQueryData(key, complete);
      }
      let finish!: (rows: typeof complete) => void;
      const load = vi.fn(
        () =>
          new Promise<typeof complete>((resolve) => {
            finish = resolve;
          }),
      );
      function SessionsObserver() {
        useQuery({ queryKey: currentKey, queryFn: load, staleTime: Infinity });
        return null;
      }
      const publishedLengths: number[] = [];
      const unsubscribe = queryClient.getQueryCache().subscribe(() => {
        publishedLengths.push(queryClient.getQueryData<typeof complete>(currentKey)!.length);
      });
      render(
        <QueryClientProvider client={queryClient}>
          <RealtimeEventsProvider>
            <SessionsObserver />
          </RealtimeEventsProvider>
        </QueryClientProvider>,
      );
      await act(async () => {});
      expect(load).not.toHaveBeenCalled();
      const socket = FakeWebSocket.instances[0]!;
      await act(async () => {
        socket.emitMessage({
          type,
          channel: "sessions",
          event: "sessions.replaced",
          data: complete.slice(0, 200),
        });
      });
      expect(load).toHaveBeenCalledTimes(1);
      expect(queryClient.getQueryData(currentKey)).toEqual(complete);
      expect(queryClient.getQueryState(otherKey)?.isInvalidated).toBe(false);
      expect(queryClient.getQueryState(adminKeys.sessions())?.isInvalidated).toBe(false);
      const refreshed = complete.map((row) => ({ id: `fresh-${row.id}` }));
      await act(async () => {
        finish(refreshed);
      });
      expect(queryClient.getQueryData(currentKey)).toEqual(refreshed);
      expect(queryClient.getQueryData(otherKey)).toEqual(complete);
      expect(queryClient.getQueryData(adminKeys.sessions())).toEqual(complete);
      expect(publishedLengths.every((length) => length === 205)).toBe(true);
      setProfileToken("replacement-proof");
      await act(async () => {
        socket.emitMessage({ type, channel: "sessions", event: "sessions.replaced", data: [] });
      });
      expect(load).toHaveBeenCalledTimes(1);
      expect(queryClient.getQueryState(currentKey)?.isInvalidated).toBe(false);
      expect(queryClient.getQueryData(currentKey)).toEqual(refreshed);
      unsubscribe();
    },
  );

  it("defers session refresh only for the captured scoped query on an inactive dashboard", async () => {
    setProfileId("primary");
    mockState.profile = { id: "primary", has_pin: false };
    mockState.pathname = "/admin";
    mockState.pageActivity.canPollDashboard = false;
    const authority = captureProfileRequestContext()!;
    const currentKey = adminSessionsKey(authority);
    const otherKey = adminSessionsKey({ ...authority, profileId: "other" });
    const queryClient = new QueryClient();
    for (const key of [currentKey, otherKey, adminKeys.sessions()])
      queryClient.setQueryData(key, []);
    render(
      <QueryClientProvider client={queryClient}>
        <RealtimeEventsProvider>
          <div />
        </RealtimeEventsProvider>
      </QueryClientProvider>,
    );
    await act(async () => {});
    await act(async () => {
      FakeWebSocket.instances[0]!.emitMessage({
        type: "snapshot",
        channel: "sessions",
        data: [{ id: "running" }],
      });
    });
    expect(queryClient.getQueryData(currentKey)).toEqual([]);
    expect(queryClient.getQueryState(currentKey)?.isInvalidated).toBe(true);
    expect(queryClient.getQueryState(otherKey)?.isInvalidated).toBe(false);
    expect(queryClient.getQueryState(adminKeys.sessions())?.isInvalidated).toBe(false);
  });

  it("coalesces session events on a movie page and stops refreshing when events stop", async () => {
    setProfileId("primary");
    mockState.profile = { id: "primary", has_pin: false };
    mockState.pathname = "/item/movie-1";
    const queryClient = new QueryClient();
    const key = adminSessionsKey(captureProfileRequestContext());
    const load = vi.fn(async () => []);
    queryClient.setQueryData(key, []);
    function SessionsObserver() {
      useQuery({ queryKey: key, queryFn: load, staleTime: Infinity });
      return null;
    }
    const view = render(
      <QueryClientProvider client={queryClient}>
        <RealtimeEventsProvider>
          <SessionsObserver />
        </RealtimeEventsProvider>
      </QueryClientProvider>,
    );
    await act(async () => {});
    const emit = () =>
      FakeWebSocket.instances[0]!.emitMessage({
        type: "event",
        channel: "sessions",
        event: "sessions.replaced",
        data: [],
      });
    for (let i = 0; i < 20; i++)
      await act(async () => {
        emit();
      });
    expect(load).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(load).toHaveBeenCalledTimes(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(load).toHaveBeenCalledTimes(2);

    await act(async () => {
      emit();
    });
    await act(async () => {
      emit();
    });
    expect(load).toHaveBeenCalledTimes(3);
    view.unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(load).toHaveBeenCalledTimes(3);
  });

  it.each(["running", "cancelling"])(
    "keeps HTTP task state when a remote node reports %s",
    async (state) => {
      const task = {
        key: "refresh_metadata",
        state: "idle",
        progress: 0,
        execution_scope: "process",
      };
      const client = new QueryClient();
      const load = vi.fn(async () => [task]);
      client.setQueryData(adminKeys.tasks(), [task]);
      function TaskObserver() {
        useQuery({ queryKey: adminKeys.tasks(), queryFn: load, staleTime: Infinity });
        return null;
      }
      render(
        <QueryClientProvider client={client}>
          <RealtimeEventsProvider>
            <TaskObserver />
          </RealtimeEventsProvider>
        </QueryClientProvider>,
      );
      await act(async () => {});
      await act(async () => {
        FakeWebSocket.instances[0]!.emitMessage({
          type: "event",
          channel: "tasks",
          event: "task.updated",
          data: { key: task.key, state, progress: 40, triggers: [] },
        });
      });
      expect(client.getQueryData(adminKeys.tasks())).toEqual([task]);
      expect(load).toHaveBeenCalledTimes(1);
    },
  );

  it("coalesces task reads and refreshes history only on completion or reconnect", async () => {
    const task: TaskInfo = {
      key: "refresh_metadata",
      name: "Refresh metadata",
      description: "",
      category: "metadata",
      state: "idle",
      progress: 0,
      manual_only: false,
      triggers: [],
      execution_scope: "process",
    };
    let serverTask = task;
    const client = new QueryClient();
    const list = vi.fn(async () => [serverTask]);
    const detail = vi.fn(async () => serverTask);
    const history = vi.fn(async () => []);
    const metrics = vi.fn(async () => ({}));
    const other = vi.fn(async () => ({ ...task, key: "other" }));
    const queries = [
      { queryKey: adminKeys.tasks(), queryFn: list, data: [task] },
      { queryKey: adminKeys.task(task.key), queryFn: detail, data: task },
      { queryKey: [...adminKeys.taskHistory(task.key), "pages"], queryFn: history, data: [] },
      { queryKey: adminKeys.taskMetrics(task.key), queryFn: metrics, data: {} },
      { queryKey: adminKeys.task("other"), queryFn: other, data: { ...task, key: "other" } },
    ];
    for (const query of queries) client.setQueryData(query.queryKey, query.data);
    function Observer({ index }: { index: number }) {
      const { queryKey, queryFn } = queries[index]!;
      useQuery<unknown>({ queryKey, queryFn, staleTime: Infinity });
      return null;
    }
    render(
      <QueryClientProvider client={client}>
        <RealtimeEventsProvider>
          {queries.map((_, index) => (
            <Observer key={index} index={index} />
          ))}
        </RealtimeEventsProvider>
      </QueryClientProvider>,
    );
    await act(async () => {});
    const emit = (state: string) =>
      FakeWebSocket.instances[0]!.emitMessage({
        type: "event",
        channel: "tasks",
        event: "task.updated",
        data: { key: task.key, state, progress: 99, triggers: [] },
      });
    for (let i = 0; i < 20; i++)
      await act(async () => {
        emit("running");
      });
    expect(list).toHaveBeenCalledTimes(1);
    expect(detail).toHaveBeenCalledTimes(1);
    expect(history).not.toHaveBeenCalled();
    expect(metrics).not.toHaveBeenCalled();
    expect(other).not.toHaveBeenCalled();
    expect(client.getQueryData(adminKeys.tasks())).toEqual([task]);
    serverTask = {
      ...task,
      state: "running",
      progress: 20,
      triggers: [{ type: "interval", interval_ms: 60_000 }],
      next_run_at: "2026-01-02T03:04:05Z",
    };
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(list).toHaveBeenCalledTimes(2);
    expect(detail).toHaveBeenCalledTimes(2);
    expect(client.getQueryData(adminKeys.tasks())).toEqual([serverTask]);
    expect(client.getQueryData(adminKeys.task(task.key))).toEqual(serverTask);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(30_000);
    });
    expect(list).toHaveBeenCalledTimes(2);
    serverTask = task;
    await act(async () => {
      emit("idle");
    });
    expect(list).toHaveBeenCalledTimes(3);
    expect(detail).toHaveBeenCalledTimes(3);
    expect(history).toHaveBeenCalledTimes(1);
    expect(metrics).toHaveBeenCalledTimes(1);
    expect(other).not.toHaveBeenCalled();
    await act(async () => {
      FakeWebSocket.instances[0]!.emitMessage({ type: "snapshot", channel: "tasks", data: [] });
      await vi.advanceTimersByTimeAsync(5_000);
    });
    expect(list).toHaveBeenCalledTimes(4);
    expect(detail).toHaveBeenCalledTimes(4);
    expect(history).toHaveBeenCalledTimes(2);
    expect(metrics).toHaveBeenCalledTimes(2);
    expect(other).toHaveBeenCalledTimes(1);
  });

  it.each([false, true])(
    "catches up a slow task read after reconnect (cached: %s)",
    async (cached) => {
      const client = new QueryClient();
      const oldTask = { key: "refresh_metadata", state: "running", progress: 10 };
      const finishedTask = { ...oldTask, state: "idle", progress: 0 };
      if (cached) client.setQueryData(adminKeys.tasks(), [oldTask]);
      let finishOld!: (rows: (typeof oldTask)[]) => void;
      const load = vi
        .fn()
        .mockImplementationOnce(
          () =>
            new Promise((resolve) => {
              finishOld = resolve;
            }),
        )
        .mockResolvedValue([finishedTask]);
      function TaskObserver() {
        useQuery({ queryKey: adminKeys.tasks(), queryFn: load });
        return null;
      }
      render(
        <QueryClientProvider client={client}>
          <RealtimeEventsProvider>
            <TaskObserver />
          </RealtimeEventsProvider>
        </QueryClientProvider>,
      );
      await act(async () => {});
      expect(load).toHaveBeenCalledTimes(1);
      await act(async () => {
        FakeWebSocket.instances[0]!.emitMessage({
          type: "snapshot",
          channel: "tasks",
          data: [finishedTask],
        });
        for (let i = 0; i < 20; i++)
          FakeWebSocket.instances[0]!.emitMessage({
            type: "event",
            channel: "tasks",
            event: "task.updated",
            data: oldTask,
          });
        await vi.advanceTimersByTimeAsync(20_000);
      });
      expect(load).toHaveBeenCalledTimes(1);
      await act(async () => {
        finishOld([oldTask]);
        await vi.advanceTimersByTimeAsync(5_000);
      });
      expect(load).toHaveBeenCalledTimes(2);
      expect(client.getQueryData(adminKeys.tasks())).toEqual([finishedTask]);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(load).toHaveBeenCalledTimes(2);
    },
  );

  it("refreshes task completion while the visible dashboard is unfocused", async () => {
    mockState.pathname = "/admin";
    mockState.pageActivity.isFocused = false;
    mockState.pageActivity.canPollDashboard = false;
    const client = new QueryClient({
      defaultOptions: { queries: { refetchOnWindowFocus: false } },
    });
    const task = { key: "refresh_metadata", state: "running", progress: 10 };
    const finishedTask = { ...task, state: "idle", progress: 0 };
    const load = vi.fn(async () => [finishedTask]);
    client.setQueryData(adminKeys.tasks(), [task]);
    function TaskObserver() {
      useQuery({ queryKey: adminKeys.tasks(), queryFn: load, staleTime: Infinity });
      return null;
    }
    render(
      <QueryClientProvider client={client}>
        <RealtimeEventsProvider>
          <TaskObserver />
        </RealtimeEventsProvider>
      </QueryClientProvider>,
    );
    await act(async () => {});
    await act(async () => {
      FakeWebSocket.instances[0]!.emitMessage({
        type: "event",
        channel: "tasks",
        event: "task.updated",
        data: finishedTask,
      });
    });
    expect(load).toHaveBeenCalledTimes(1);
    expect(client.getQueryData(adminKeys.tasks())).toEqual([finishedTask]);
  });

  it("defers broad catch-up refetches until foreground playback exits", async () => {
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

    await act(async () => {
      mockState.pageActivity = {
        ...mockState.pageActivity,
        isVisible: false,
        canApplyRealtimeUpdates: false,
      };
      view.rerender(provider());
    });

    await act(async () => {
      mockState.pageActivity = {
        ...mockState.pageActivity,
        isVisible: true,
        canApplyRealtimeUpdates: true,
      };
      view.rerender(provider());
    });

    expect(refetchQueries).not.toHaveBeenCalled();

    await act(async () => {
      mockState.pathname = "/item/movie-1";
      view.rerender(provider());
    });

    expect(refetchQueries).toHaveBeenCalledTimes(1);
    expect(refetchQueries).toHaveBeenCalledWith({
      type: "active",
      predicate: expect.any(Function),
    });
  });

  it("preserves cached watched state when a favorite-only event arrives", async () => {
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

    await act(async () => {});
    await act(async () => {
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
});
