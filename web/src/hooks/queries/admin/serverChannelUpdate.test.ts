import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority, notificationScope } from "@/api/v2/notifications";
import { adminKeys } from "../keys";
import { useUpdateServerNotificationChannel } from "./serverNotificationChannels";

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

it("invalidates only the captured server channel cache and rejects a stale receipt", async () => {
  const client = new QueryClient();
  const base = adminKeys.serverNotificationChannels();
  const own = [...base, notificationScope(captureNotificationAuthority())];
  const other = [...base, "other"];
  for (const key of [base, own, other]) client.setQueryData(key, [{ id: "one" }]);
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    new Response(JSON.stringify({ id: "one", name: "updated", etag: '"new"' }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useUpdateServerNotificationChannel(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  await act(async () => {
    await result.current.mutateAsync({ id: "one", name: "updated" });
  });
  expect(client.getQueryState(own)?.isInvalidated).toBe(true);
  for (const key of [base, other]) expect(client.getQueryState(key)?.isInvalidated).toBe(false);
  client.setQueryData(own, [{ id: "two" }]);
  fetch.mockImplementation(async () => {
    setProfileToken("new-pin");
    return new Response(JSON.stringify({ id: "one", name: "updated", etag: '"new"' }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  await act(async () => {
    await expect(result.current.mutateAsync({ id: "two", name: "updated" })).rejects.toThrow();
  });
  expect(client.getQueryState(own)?.isInvalidated).toBe(false);
  expect(fetch).toHaveBeenCalledTimes(2);
  client.clear();
});

it("sends exact server channel update once on failure", async () => {
  const client = new QueryClient();
  const { result } = renderHook(() => useUpdateServerNotificationChannel(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  for (const status of [401, 403, 500]) {
    const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    await act(async () => {
      await expect(
        result.current.mutateAsync({ id: "row-one", name: "updated" }),
      ).rejects.toThrow();
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(String(fetch.mock.calls[0]![0])).toContain(
      "/api/v2/admin/notifications/server-channels/row-one",
    );
    expect(fetch.mock.calls[0]![1].method).toBe("PUT");
  }
  client.clear();
});

it("refuses a paused update after authority replacement", async () => {
  const client = new QueryClient();
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const { result, rerender } = renderHook(() => useUpdateServerNotificationChannel(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  onlineManager.setOnline(false);
  let completion: Promise<unknown>;
  act(() => {
    completion = result.current
      .mutateAsync({ id: "original-id", name: "updated" })
      .catch((error) => error);
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

it("preserves the invocation body while paused", async () => {
  const client = new QueryClient();
  const fetch = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ id: "row-one", etag: '"next"' }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  const { result, rerender } = renderHook(() => useUpdateServerNotificationChannel(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  onlineManager.setOnline(false);
  const input = { id: "row-one", name: "original name", enabled: false };
  act(() => {
    result.current.mutate(input);
  });
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  input.name = "replacement name";
  input.enabled = true;
  rerender();
  await act(async () => {
    onlineManager.setOnline(true);
    await client.resumePausedMutations();
  });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetch).toHaveBeenCalledTimes(1);
  const request = fetch.mock.calls[0]![1];
  expect(JSON.parse(request.body)).toEqual({ name: "original name", enabled: false });
  client.clear();
});
