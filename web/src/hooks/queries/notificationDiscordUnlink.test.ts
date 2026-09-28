import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority, notificationScope } from "@/api/v2/notifications";
import { notificationKeys } from "./keys";
import { useUnlinkDiscord } from "./notifications";

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

it("invalidates only the captured Discord preferences cache and rejects a stale receipt", async () => {
  const client = new QueryClient();
  const base = notificationKeys.discordPreferences();
  const own = [...base, notificationScope(captureNotificationAuthority())];
  const other = [...base, "other"];
  for (const key of [base, own, other]) client.setQueryData(key, [{ id: "one" }]);
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValue(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useUnlinkDiscord(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  await act(async () => {
    await result.current.mutateAsync();
  });
  expect(client.getQueryState(own)?.isInvalidated).toBe(true);
  for (const key of [base, other]) expect(client.getQueryState(key)?.isInvalidated).toBe(false);
  client.setQueryData(own, [{ id: "two" }]);
  fetch.mockImplementation(async () => {
    setProfileToken("new-pin");
    return new Response(null, { status: 204 });
  });
  await act(async () => {
    await expect(result.current.mutateAsync()).rejects.toThrow();
  });
  expect(client.getQueryState(own)?.isInvalidated).toBe(false);
  expect(fetch).toHaveBeenCalledTimes(2);
  client.clear();
});

it("does not replay unlink on authentication or service failure", async () => {
  const client = new QueryClient();
  const { result } = renderHook(() => useUnlinkDiscord(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  for (const status of [401, 403, 500]) {
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    await act(async () => {
      await expect(result.current.mutateAsync()).rejects.toThrow();
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(String(fetch.mock.calls[0]![0])).toContain("/api/v2/notifications/discord-link");
    expect(fetch.mock.calls[0]![1].method).toBe("DELETE");
  }
  client.clear();
});
