import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority, notificationScope } from "@/api/v2/notifications";
import { notificationKeys } from "./keys";
import { useDeleteWebPushSubscription } from "./notificationWebhooks";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("invalidates only the captured subscription cache and rejects a stale receipt", async () => {
  const client = new QueryClient();
  const base = notificationKeys.webPushSubscriptions();
  const own = [...base, notificationScope(captureNotificationAuthority())];
  const other = [...base, "other"];
  for (const key of [base, own, other]) client.setQueryData(key, [{ id: "one" }]);
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useDeleteWebPushSubscription(), {
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
    return new Response(null, { status: 204 });
  });
  await act(async () => {
    await expect(result.current.mutateAsync("two")).rejects.toThrow();
  });
  expect(client.getQueryState(own)?.isInvalidated).toBe(false);
  expect(fetch).toHaveBeenCalledTimes(2);
  client.clear();
});
