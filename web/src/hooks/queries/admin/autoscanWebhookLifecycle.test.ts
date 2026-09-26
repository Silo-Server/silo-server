import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  captureAutoscanWebhookIntent,
  useCreateAutoscanWebhook,
  useRotateAutoscanWebhook,
  useDeleteAutoscanWebhook,
} from "../useAutoscan";
const response = (action: string) =>
  action === "delete"
    ? new Response(null, { status: 204 })
    : new Response(
        JSON.stringify({
          id: "source-a",
          webhook_configured: true,
          webhook_url: "/api/v2/autoscan/webhooks/synthetic",
        }),
        { headers: { "Content-Type": "application/json" } },
      );
function fixture() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3, retryDelay: 0 }, queries: { retry: false } },
  });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-a");
  setProfileToken("pin-a");
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
});

for (const [action, useHook] of [
  ["create", useCreateAutoscanWebhook],
  ["rotate", useRotateAutoscanWebhook],
  ["delete", useDeleteAutoscanWebhook],
] as const) {
  it(`${action} retains original offline target and sends exactly once without body`, async () => {
    onlineManager.setOnline(false);
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(response(action)));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useHook(), fixture());
    act(() => result.current.mutate("source-a"));
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    act(() => onlineManager.setOnline(true));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
      `/api/v2/admin/autoscan/sources/source-a/webhook${action === "rotate" ? "/rotate" : ""}`,
    );
    expect(fetchMock.mock.calls[0]?.[1].method).toBe(action === "delete" ? "DELETE" : "POST");
    expect(fetchMock.mock.calls[0]?.[1].body).toBeUndefined();
    expect(result.current.data).toEqual(
      action === "delete"
        ? null
        : expect.objectContaining({ id: "source-a", webhook_configured: true }),
    );
  });
  it(`${action} refuses queued authority replacement`, async () => {
    onlineManager.setOnline(false);
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useHook(), fixture());
    act(() => result.current.mutate("source-a"));
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    act(() => {
      setProfileToken("pin-b");
      onlineManager.setOnline(true);
    });
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).not.toHaveBeenCalled();
  });
  it.each(["401", "network"])(`${action} never replays %s under global retry3`, async (failure) => {
    const fetchMock =
      failure === "network"
        ? vi.fn().mockRejectedValue(new Error("private-token"))
        : vi.fn().mockImplementation(() =>
            Promise.resolve(
              new Response(
                JSON.stringify({
                  type: "https://silo.dev/problems/authentication_required",
                  title: "Unauthorized",
                  status: 401,
                }),
                { status: 401, headers: { "Content-Type": "application/problem+json" } },
              ),
            ),
          );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useHook(), fixture());
    act(() => result.current.mutate("source-a"));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
  });
  it(`${action} fences late receipts, callbacks and cache invalidation`, async () => {
    let release!: (r: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((resolve) => {
            release = resolve;
          }),
      ),
    );
    const { client, wrapper } = fixture();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const callback = vi.fn();
    const { result } = renderHook(() => useHook(), { wrapper });
    act(() => result.current.mutate("source-a", { onSuccess: callback, onError: callback }));
    await waitFor(() => expect(release).toBeTypeOf("function"));
    act(() => setProfileId("profile-b"));
    await act(async () => release(response(action)));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(callback).not.toHaveBeenCalled();
    expect(invalidate).not.toHaveBeenCalled();
    expect(result.current.data).toBeUndefined();
  });
}

it("a stale rendered authority cannot dispatch or invoke a caller callback", async () => {
  const intent = captureAutoscanWebhookIntent("source-a");
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const callback = vi.fn();
  const { result } = renderHook(() => useCreateAutoscanWebhook(intent.profileContext), fixture());
  act(() => setProfileToken("pin-b"));
  act(() => result.current.mutate("source-a", { onError: callback, onSuccess: callback }));
  expect(fetchMock).not.toHaveBeenCalled();
  expect(callback).not.toHaveBeenCalled();
});
