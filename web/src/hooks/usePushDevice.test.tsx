import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { apiMock, cacheVapidMock } = vi.hoisted(() => ({
  apiMock: vi.fn(),
  cacheVapidMock: vi.fn(),
}));
vi.mock("@/api/client", () => ({ api: apiMock }));
vi.mock("@/lib/push", async (orig) => ({
  ...(await orig<typeof import("@/lib/push")>()),
  cacheVapidKey: cacheVapidMock,
}));

import { usePushDevice } from "./usePushDevice";

function wrap() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: React.ReactNode }) => (
    <QueryClientProvider client={qc}>{children}</QueryClientProvider>
  );
}

let subscribeMock: ReturnType<typeof vi.fn>;
let getSubMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
  apiMock.mockReset();
  cacheVapidMock.mockReset();
  subscribeMock = vi.fn(async () => ({ toJSON: () => ({ endpoint: "https://x", keys: {} }) }));
  getSubMock = vi.fn(async () => null);
  (globalThis as any).PushManager = function () {};
  (navigator as any).serviceWorker = {
    register: vi.fn(),
    ready: Promise.resolve({ pushManager: { subscribe: subscribeMock, getSubscription: getSubMock } }),
  };
  (globalThis as any).Notification = { permission: "default", requestPermission: vi.fn(async () => "granted") };
});
afterEach(() => vi.restoreAllMocks());

describe("usePushDevice", () => {
  it("reports unsupported when PushManager missing", () => {
    delete (globalThis as any).PushManager;
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    expect(result.current.status).toBe("unsupported");
  });

  it("reports blocked when permission denied", () => {
    (globalThis as any).Notification.permission = "denied";
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    expect(result.current.status).toBe("blocked");
  });

  it("enable(): permission → key → subscribe → PUT device, caches key, status on", async () => {
    apiMock.mockImplementation((path: string) => {
      if (path.includes("webpush-key")) return Promise.resolve({ vapid_public_key: "AQID" });
      return Promise.resolve(undefined); // PUT device
    });
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    await act(async () => { await result.current.enable(); });
    expect(subscribeMock).toHaveBeenCalledOnce();
    expect(cacheVapidMock).toHaveBeenCalledWith("AQID");
    const putCall = apiMock.mock.calls.find((c) => c[0] === "/notifications/push/device");
    expect(putCall?.[1]).toMatchObject({ method: "PUT" });
    expect(JSON.parse(putCall![1].body)).toMatchObject({ transport: "webpush" });
    await waitFor(() => expect(result.current.status).toBe("on"));
  });

  it("enable(): permission denied short-circuits, no subscribe", async () => {
    (globalThis as any).Notification.requestPermission = vi.fn(async () => "denied");
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    await act(async () => { await result.current.enable(); });
    expect(subscribeMock).not.toHaveBeenCalled();
    expect(result.current.status).toBe("blocked");
  });

  it("disable(): unsubscribe + DELETE, status off", async () => {
    const unsub = vi.fn(async () => true);
    getSubMock.mockResolvedValue({ unsubscribe: unsub });
    apiMock.mockResolvedValue(undefined);
    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });
    await act(async () => { await result.current.disable(); });
    expect(unsub).toHaveBeenCalled();
    const del = apiMock.mock.calls.find((c) => c[0] === "/notifications/push/device" && c[1]?.method === "DELETE");
    expect(del).toBeTruthy();
    await waitFor(() => expect(result.current.status).toBe("off"));
  });

  it("generation guard: enable() superseded by disable() — disable wins, stale enable cannot clobber", async () => {
    // Hold enable()'s webpush-key fetch so we can let disable() run to completion first.
    let rejectKey!: (e: unknown) => void;
    const keyPromise = new Promise<{ vapid_public_key: string }>((_, rej) => {
      rejectKey = rej;
    });

    apiMock.mockImplementation((path: string, opts?: { method?: string }) => {
      if (path.includes("webpush-key")) return keyPromise;
      if (path.includes("device") && opts?.method === "DELETE") return Promise.resolve(undefined);
      return Promise.resolve(undefined);
    });

    const { result } = renderHook(() => usePushDevice(), { wrapper: wrap() });

    // Start enable() without awaiting — it stalls at the webpush-key call (gen=1).
    let innerEnableResolve!: () => void;
    const innerEnableDone = new Promise<void>((res) => { innerEnableResolve = res; });
    act(() => {
      void result.current.enable().then(innerEnableResolve, innerEnableResolve);
    });

    // Drain microtasks so enable() reaches the webpush-key await (past requestPermission + sw.ready).
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });

    // Run disable() to full completion — increments gen to 2, sets status "off".
    await act(async () => { await result.current.disable(); });

    // Reject the stale enable()'s webpush-key call — gen=1 is superseded so
    // the catch's set("off") is a no-op; disable's "off" (gen=2) must survive.
    await act(async () => {
      rejectKey(new Error("stale"));
      await innerEnableDone;
    });

    // disable() (gen=2) is the last winner; status must remain "off".
    expect(result.current.status).toBe("off");
  });
});
