import { act, cleanup, renderHook } from "@testing-library/react";
import { useMemo } from "react";
import { afterEach, expect, it, vi } from "vitest";

import { useAdminLogStream } from "./useAdminLogStream";

const { mint, deferred } = vi.hoisted(() => ({
  mint: vi.fn(),
  deferred: { params: null as { method: string } | null },
}));
vi.mock("react", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react")>();
  return {
    ...actual,
    useDeferredValue: (value: unknown) => {
      const current = actual.useDeferredValue(value);
      return deferred.params ?? current;
    },
  };
});
vi.mock("@/api/client", () => ({
  captureProfileRequestContext: () => ({ profileId: "admin-profile" }),
  isCapturedProfileAuthorityActive: () => true,
}));
vi.mock("@/api/v2/adminLogsSocket", () => ({
  mintAdminLogsSocketTicket: mint,
  buildAdminLogsSocketQuery: (params: { method: string }) => `method=${params.method}`,
  buildAdminLogsSocketUrl: () => "ws://example.test/logs",
  adminLogsSocketProtocols: () => [],
}));

class FakeWebSocket {
  static OPEN = 1;
  static CONNECTING = 0;
  static instances: FakeWebSocket[] = [];
  readyState = FakeWebSocket.OPEN;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  onclose: (() => void) | null = null;
  constructor() {
    FakeWebSocket.instances.push(this);
  }
  close() {
    this.readyState = 3;
  }
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  mint.mockReset();
  deferred.params = null;
  FakeWebSocket.instances = [];
});

it("hides previous filter matches after a failed handshake and accepts a fresh snapshot on recovery", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", FakeWebSocket);
  mint.mockResolvedValue({ ticket: "test-ticket" });
  const { result, rerender } = renderHook(
    ({ method }) => {
      const params = useMemo(() => ({ method }), [method]);
      return useAdminLogStream("audit", params, true);
    },
    { initialProps: { method: "PUT" } },
  );
  await act(async () => {
    await vi.advanceTimersByTimeAsync(250);
  });
  act(() => {
    const socket = FakeWebSocket.instances[0];
    if (!socket) throw new Error("Initial log socket was not created");
    socket.onmessage?.({
      data: JSON.stringify({
        type: "snapshot",
        stream: "audit",
        entries: [{ id: 7, method: "PUT" }],
        next_cursor: "old-page",
      }),
    });
  });
  expect(result.current.rows.map((row) => row.id)).toEqual([7]);
  expect(result.current.nextCursor).toBe("old-page");

  mint.mockRejectedValue(new Error("ticket service unavailable"));
  rerender({ method: "POST" });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(250);
  });
  expect(result.current.error).toBe("Unable to open log stream.");
  expect(result.current.rows).toEqual([]);
  expect(result.current.nextCursor).toBeUndefined();

  mint.mockResolvedValue({ ticket: "replacement-ticket" });
  act(() => result.current.reconnect());
  await act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
  act(() => {
    const socket = FakeWebSocket.instances[1];
    if (!socket) throw new Error("Recovery log socket was not created");
    socket.onmessage?.({
      data: JSON.stringify({
        type: "snapshot",
        stream: "audit",
        entries: [{ id: 8, method: "POST" }],
      }),
    });
  });
  expect(result.current.rows.map((row) => row.id)).toEqual([8]);
  expect(result.current.error).toBeUndefined();
});

it("hides rows and cursor immediately while the connection filter is deferred", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", FakeWebSocket);
  mint.mockResolvedValue({ ticket: "test-ticket" });
  // Stable objects, so holding the deferred value keeps the old connection open.
  const paramsByMethod = { PUT: { method: "PUT" }, POST: { method: "POST" } };
  const { result, rerender } = renderHook(
    ({ method }: { method: "PUT" | "POST" }) =>
      useAdminLogStream("audit", paramsByMethod[method], true),
    { initialProps: { method: "PUT" } },
  );
  await act(async () => vi.advanceTimersByTimeAsync(250));
  act(() => {
    FakeWebSocket.instances[0]?.onopen?.();
    FakeWebSocket.instances[0]?.onmessage?.({
      data: JSON.stringify({
        type: "snapshot",
        stream: "audit",
        entries: [{ id: 7, method: "PUT" }],
        next_cursor: "old-page",
      }),
    });
  });
  expect(result.current.rows.map((row) => row.id)).toEqual([7]);
  expect(result.current.nextCursor).toBe("old-page");

  deferred.params = paramsByMethod.PUT;
  rerender({ method: "POST" });
  expect(result.current.rows).toEqual([]);
  expect(result.current.nextCursor).toBeUndefined();
  expect(result.current.isConnecting).toBe(true);
  expect(mint).toHaveBeenCalledTimes(1);

  deferred.params = null;
  rerender({ method: "POST" });
  await act(async () => vi.advanceTimersByTimeAsync(250));
  act(() => {
    FakeWebSocket.instances[1]?.onmessage?.({
      data: JSON.stringify({
        type: "snapshot",
        stream: "audit",
        entries: [{ id: 8, method: "POST" }],
      }),
    });
  });
  expect(result.current.rows.map((row) => row.id)).toEqual([8]);
});

it("reports a filter change as connecting until the new selection's snapshot arrives", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", FakeWebSocket);
  mint.mockResolvedValue({ ticket: "test-ticket" });
  const { result, rerender } = renderHook(
    ({ method }) => {
      const params = useMemo(() => ({ method }), [method]);
      return useAdminLogStream("audit", params, true);
    },
    { initialProps: { method: "PUT" } },
  );
  await act(async () => vi.advanceTimersByTimeAsync(250));
  act(() => {
    const socket = FakeWebSocket.instances[0];
    if (!socket) throw new Error("Initial log socket was not created");
    socket.onopen?.();
    socket.onmessage?.({
      data: JSON.stringify({ type: "snapshot", stream: "audit", entries: [{ id: 7 }] }),
    });
  });
  expect(result.current.isLive).toBe(true);

  rerender({ method: "POST" });
  await act(async () => vi.advanceTimersByTimeAsync(100));
  expect(mint).toHaveBeenCalledTimes(1);
  expect(result.current.rows).toEqual([]);
  expect(result.current.isConnecting).toBe(true);
  expect(result.current.connectionState).toBe("connecting");

  await act(async () => vi.advanceTimersByTimeAsync(150));
  act(() => FakeWebSocket.instances[1]?.onopen?.());
  expect(result.current.isLive).toBe(true);
  expect(result.current.isConnecting).toBe(true);

  act(() => {
    FakeWebSocket.instances[1]?.onmessage?.({
      data: JSON.stringify({ type: "snapshot", stream: "audit", entries: [] }),
    });
  });
  expect(result.current.rows).toEqual([]);
  expect(result.current.isConnecting).toBe(false);
});

it("keeps debouncing filter changes after an explicit reconnect", async () => {
  vi.useFakeTimers();
  vi.stubGlobal("WebSocket", FakeWebSocket);
  mint.mockResolvedValue({ ticket: "test-ticket" });
  const { result, rerender } = renderHook(
    ({ method }) => {
      const params = useMemo(() => ({ method }), [method]);
      return useAdminLogStream("audit", params, true);
    },
    { initialProps: { method: "PUT" } },
  );
  await act(async () => vi.advanceTimersByTimeAsync(250));
  expect(mint).toHaveBeenCalledTimes(1);

  act(() => result.current.reconnect());
  await act(async () => vi.advanceTimersByTimeAsync(0));
  expect(mint).toHaveBeenCalledTimes(2);

  for (const method of ["PO", "POS", "POST"]) {
    rerender({ method });
    await act(async () => vi.advanceTimersByTimeAsync(0));
  }
  expect(mint).toHaveBeenCalledTimes(2);

  await act(async () => vi.advanceTimersByTimeAsync(249));
  expect(mint).toHaveBeenCalledTimes(2);
  await act(async () => vi.advanceTimersByTimeAsync(1));
  expect(mint).toHaveBeenCalledTimes(3);

  act(() => result.current.reconnect());
  await act(async () => vi.advanceTimersByTimeAsync(0));
  expect(mint).toHaveBeenCalledTimes(4);
});
