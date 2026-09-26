import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority, notificationScope } from "@/api/v2/notifications";
import { notificationKeys } from "./keys";
import { useRotateNotificationWebhookSecret } from "./notificationWebhooks";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => {
  onlineManager.setOnline(true);
  cleanup();
  vi.unstubAllGlobals();
});

it("invalidates only the captured webhook cache and rejects a stale receipt", async () => {
  const client = new QueryClient();
  const base = notificationKeys.webhooks();
  const own = [...base, notificationScope(captureNotificationAuthority())];
  const other = [...base, "other"];
  for (const key of [base, own, other]) client.setQueryData(key, [{ id: "one" }]);
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    new Response(JSON.stringify({ signing_secret: "synthetic-secret" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useRotateNotificationWebhookSecret(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  await act(async () => {
    await result.current.mutateAsync("one");
  });
  expect(client.getQueryState(own)?.isInvalidated).toBe(true);
  for (const key of [base, other]) expect(client.getQueryState(key)?.isInvalidated).toBe(false);
  client.setQueryData(own, [{ id: "two" }]);
  fetch.mockImplementation(async () => {
    setProfileToken("new-pin");
    return new Response(JSON.stringify({ signing_secret: "synthetic-secret" }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  await act(async () => {
    await expect(result.current.mutateAsync("two")).rejects.toThrow();
  });
  expect(client.getQueryState(own)?.isInvalidated).toBe(false);
  expect(fetch).toHaveBeenCalledTimes(2);
  client.clear();
});

it("sends exact webhook rotation once on failure", async () => {
  const client = new QueryClient();
  const { result } = renderHook(() => useRotateNotificationWebhookSecret(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  for (const status of [401, 403, 500]) {
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    await act(async () => {
      await expect(result.current.mutateAsync("row-one")).rejects.toThrow();
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(String(fetch.mock.calls[0]![0])).toContain(
      "/api/v2/notifications/webhooks/row-one/rotate-secret",
    );
    expect(fetch.mock.calls[0]![1].method).toBe("POST");
  }
  client.clear();
});

it("refuses a paused rotation after authority replacement", async () => {
  const client = new QueryClient();
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const { result, rerender } = renderHook(() => useRotateNotificationWebhookSecret(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  onlineManager.setOnline(false);
  let completion: Promise<unknown>;
  act(() => {
    completion = result.current.mutateAsync("original-id").catch((error) => error);
  });
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  setProfileToken("replacement-pin");
  rerender();
  await act(async () => {
    onlineManager.setOnline(true);
    await client.resumePausedMutations();
    await completion;
  });
  expect(fetch).not.toHaveBeenCalled();
  client.clear();
});
