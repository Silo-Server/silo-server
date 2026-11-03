// Webhook and server-channel hooks each carry their own authority fence, so
// every hook keeps its own row.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken, setRefreshToken } from "@/api/client";
import { captureNotificationAuthority, notificationScope } from "@/api/v2/notifications";
import { adminKeys, notificationKeys } from "./keys";
import {
  useDeleteServerNotificationChannel,
  useRotateServerNotificationChannelSecret,
  useUpdateServerNotificationChannel,
} from "./admin/serverNotificationChannels";
import {
  useRotateNotificationWebhookSecret,
  useUpdateNotificationWebhook,
} from "./notificationWebhooks";

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

describe.each([
  {
    name: "server channel",
    useRotate: useRotateServerNotificationChannelSecret,
    base: adminKeys.serverNotificationChannels(),
    path: "/api/v2/admin/notifications/server-channels/row-one/rotate-secret",
  },
  {
    name: "webhook",
    useRotate: useRotateNotificationWebhookSecret,
    base: notificationKeys.webhooks(),
    path: "/api/v2/notifications/webhooks/row-one/rotate-secret",
  },
])("$name secret rotation", ({ useRotate, base, path }) => {
  it("invalidates only the captured cache and rejects a stale receipt", async () => {
    const client = new QueryClient();
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
    const { result } = renderHook(() => useRotate(), {
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

  it("sends the exact rotation once on failure", async () => {
    setAccessToken("synthetic-admin");
    setRefreshToken("synthetic-refresh");
    const client = new QueryClient();
    const { result } = renderHook(() => useRotate(), {
      wrapper: ({ children }: { children: ReactNode }) =>
        createElement(QueryClientProvider, { client }, children),
    });
    for (const status of [401, 500]) {
      const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
      vi.stubGlobal("fetch", fetch);
      await act(async () => {
        await expect(result.current.mutateAsync("row-one")).rejects.toThrow();
      });
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(String(fetch.mock.calls[0]![0])).toContain(path);
      expect(fetch.mock.calls[0]![1].method).toBe("POST");
    }
    client.clear();
  });
});

type Mutation = { isPaused: boolean };
type Row = { name: string; useHook: () => Mutation; send: (hook: Mutation) => Promise<unknown> };
function row<T extends Mutation>(useHook: () => T, send: (hook: T) => Promise<unknown>): Row {
  return { name: useHook.name, useHook, send: send as (hook: Mutation) => Promise<unknown> };
}
it.each([
  row(useRotateServerNotificationChannelSecret, (h) => h.mutateAsync("original-id")),
  row(useRotateNotificationWebhookSecret, (h) => h.mutateAsync("original-id")),
  row(useUpdateServerNotificationChannel, (h) =>
    h.mutateAsync({ id: "original-id", name: "updated" }),
  ),
  row(useUpdateNotificationWebhook, (h) =>
    h.mutateAsync({ id: "original-id", etag: '"original"', name: "updated" }),
  ),
  row(useDeleteServerNotificationChannel, (h) => h.mutateAsync("original-id")),
])("$name refuses a paused mutation after authority replacement", async ({ useHook, send }) => {
  const client = new QueryClient();
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  const { result, rerender } = renderHook(() => useHook(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  onlineManager.setOnline(false);
  let completion: Promise<unknown>;
  act(() => {
    completion = send(result.current).catch((error) => error);
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
