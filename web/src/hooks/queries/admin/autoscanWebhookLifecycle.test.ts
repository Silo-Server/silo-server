import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { captureAutoscanWebhookIntent, useCreateAutoscanWebhook } from "../useAutoscan";
const response = () =>
  new Response(
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

it(`create fences late receipts, callbacks and cache invalidation`, async () => {
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
  const { result } = renderHook(() => useCreateAutoscanWebhook(), { wrapper });
  act(() => result.current.mutate("source-a", { onSuccess: callback, onError: callback }));
  await waitFor(() => expect(release).toBeTypeOf("function"));
  act(() => setProfileId("profile-b"));
  await act(async () => release(response()));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(callback).not.toHaveBeenCalled();
  expect(invalidate).not.toHaveBeenCalled();
  expect(result.current.data).toBeUndefined();
});
