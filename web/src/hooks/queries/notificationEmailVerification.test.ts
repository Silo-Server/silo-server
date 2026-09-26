import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority, notificationScope } from "@/api/v2/notifications";
import { notificationKeys } from "./keys";
import { useRequestEmailNotificationAddress } from "./notifications";

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
function harness() {
  const client = new QueryClient();
  const hook = renderHook(() => useRequestEmailNotificationAddress(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  return { client, ...hook };
}
function response(id: string, current = true) {
  return new Response(
    JSON.stringify({ verification_id: id, expires_at: "2030-01-01T00:00:00Z", current }),
    { status: 200, headers: { "Content-Type": "application/json" } },
  );
}
function body(options: RequestInit | undefined) {
  return JSON.parse(String(options?.body)) as { verification_id: string; email: string };
}
it("retains uncertain intent and invalidates only its authority cache", async () => {
  const { client, result } = harness();
  const key = [
    ...notificationKeys.emailPreferences(),
    notificationScope(captureNotificationAuthority()),
  ];
  const other = [...notificationKeys.emailPreferences(), "other"];
  client.setQueryData(key, { pending_email: "" });
  client.setQueryData(other, { pending_email: "other" });
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockRejectedValueOnce(new TypeError("connection lost"))
    .mockImplementation(async (_u, o) => response(body(o).verification_id));
  vi.stubGlobal("fetch", fetch);
  await act(async () => {
    await expect(result.current.mutateAsync(" address@example.test ")).rejects.toThrow();
  });
  await act(async () => {
    await result.current.mutateAsync("address@example.test");
  });
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(fetch.mock.calls[0]![1]?.body).toBe(fetch.mock.calls[1]![1]?.body);
  expect(body(fetch.mock.calls[0]![1]).email).toBe("address@example.test");
  expect(fetch.mock.calls[1]![1]?.method).toBe("PUT");
  expect(String(fetch.mock.calls[1]![0])).toContain(
    "/api/v2/notifications/email-preferences/address",
  );
  expect(client.getQueryState(key)?.isInvalidated).toBe(true);
  expect(client.getQueryState(other)?.isInvalidated).toBe(false);
  expect(client.getQueryData(key)).toEqual({ pending_email: "" });
  await act(async () => {
    await result.current.mutateAsync("address@example.test");
  });
  expect(body(fetch.mock.calls[2]![1]).verification_id).not.toBe(
    body(fetch.mock.calls[1]![1]).verification_id,
  );
  client.clear();
});
it.each(["mutate", "mutateAsync"] as const)(
  "captures %s before offline authority replacement",
  async (method) => {
    const { client, result, rerender } = harness();
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const callback = vi.fn();
    onlineManager.setOnline(false);
    let done: Promise<unknown> | undefined;
    act(() => {
      if (method === "mutateAsync")
        done = result.current
          .mutateAsync("a@example.test", { onSuccess: callback, onError: callback })
          .catch((e) => e);
      else result.current.mutate("a@example.test", { onSuccess: callback, onError: callback });
    });
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    setProfileToken("new-pin");
    rerender();
    await act(async () => {
      onlineManager.setOnline(true);
      await client.resumePausedMutations();
      await done;
    });
    await waitFor(() => expect(result.current.isPending).toBe(false));
    expect(fetch).not.toHaveBeenCalled();
    expect(callback).not.toHaveBeenCalled();
    client.clear();
  },
);
it.each(["authority", "draft", "unmount"] as const)(
  "fences late response after %s",
  async (kind) => {
    const { client, result, rerender, unmount } = harness();
    const callback = vi.fn();
    const key = [
      ...notificationKeys.emailPreferences(),
      notificationScope(captureNotificationAuthority()),
    ];
    client.setQueryData(key, { pending_email: "old" });
    let resolve: ((r: Response) => void) | undefined;
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementationOnce(
        () =>
          new Promise((r) => {
            resolve = r;
          }),
      )
      .mockImplementation(async (_u, o) => response(body(o).verification_id));
    vi.stubGlobal("fetch", fetch);
    let done: Promise<unknown> | undefined;
    act(() => {
      done = result.current
        .mutateAsync("a@example.test", { onSuccess: callback, onError: callback })
        .catch((e) => e);
    });
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(1));
    if (kind === "authority") {
      setProfileToken("new-pin");
      rerender();
    } else if (kind === "unmount") {
      unmount();
    } else {
      await act(async () => {
        await result.current.mutateAsync("b@example.test");
      });
      client.setQueryData(key, { pending_email: "new" });
    }
    await act(async () => {
      resolve!(response(body(fetch.mock.calls[0]![1]).verification_id));
      await done;
    });
    expect(callback).not.toHaveBeenCalled();
    expect(client.getQueryState(key)?.isInvalidated).toBe(false);
    client.clear();
  },
);
it.each([401, 403, 409, 429, 500])("does not automatically replay HTTP %s", async (status) => {
  const { client, result } = harness();
  const fetch = vi.fn().mockResolvedValue(new Response(null, { status }));
  vi.stubGlobal("fetch", fetch);
  await act(async () => {
    await expect(result.current.mutateAsync("a@example.test")).rejects.toThrow();
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  client.clear();
});
it("accepts inactive receipt without resending and uses insecure-context UUID fallback", async () => {
  vi.stubGlobal("crypto", {
    getRandomValues: (a: Uint8Array) => {
      a.fill(7);
      return a;
    },
  });
  const { client, result } = harness();
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementation(async (_u, o) => response(body(o).verification_id, false));
  vi.stubGlobal("fetch", fetch);
  await act(async () => {
    expect((await result.current.mutateAsync("a@example.test")).current).toBe(false);
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(body(fetch.mock.calls[0]![1]).verification_id).toMatch(/^[0-9a-f-]{36}$/);
  client.clear();
});

it("rejects stale invocation without binding a replacement authority", async () => {
  const { client, result } = harness();
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  setProfileToken("replacement-without-render");
  await act(async () => {
    await expect(result.current.mutateAsync("a@example.test")).rejects.toThrow();
    result.current.mutate("a@example.test");
  });
  expect(fetch).not.toHaveBeenCalled();
  client.clear();
});
it("suppresses late errors and callbacks after authority replacement", async () => {
  const { client, result, rerender } = harness();
  let reject: ((e: Error) => void) | undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise((_resolve, r) => {
          reject = r;
        }),
    ),
  );
  const callback = vi.fn();
  let done: Promise<unknown> | undefined;
  act(() => {
    done = result.current.mutateAsync("a@example.test", { onError: callback }).catch((e) => e);
  });
  await waitFor(() => expect(reject).toBeDefined());
  setProfileToken("replacement");
  rerender();
  await act(async () => {
    reject!(new TypeError("connection lost"));
    await done;
  });
  expect(callback).not.toHaveBeenCalled();
  client.clear();
});

it.each(["mutate", "mutateAsync"] as const)(
  "recovers fresh same-email %s after refusing the old offline PIN intent",
  async (method) => {
    const { client, result, rerender } = harness();
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementation(async (_url, options) => response(body(options).verification_id));
    vi.stubGlobal("fetch", fetch);
    const oldCallback = vi.fn();
    onlineManager.setOnline(false);
    let oldDone: Promise<unknown> | undefined;
    act(() => {
      if (method === "mutateAsync")
        oldDone = result.current
          .mutateAsync("same@example.test", { onSuccess: oldCallback, onError: oldCallback })
          .catch((error) => error);
      else
        result.current.mutate("same@example.test", {
          onSuccess: oldCallback,
          onError: oldCallback,
        });
    });
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    const oldID = result.current.variables!.body.verification_id;
    setProfileToken("fresh-pin");
    rerender();
    await act(async () => {
      onlineManager.setOnline(true);
      await client.resumePausedMutations();
      await oldDone;
    });
    await waitFor(() => expect(result.current.isPending).toBe(false));
    expect(fetch).not.toHaveBeenCalled();
    expect(oldCallback).not.toHaveBeenCalled();
    const freshCallback = vi.fn();
    await act(async () => {
      if (method === "mutateAsync")
        await result.current.mutateAsync("same@example.test", { onSuccess: freshCallback });
      else result.current.mutate("same@example.test", { onSuccess: freshCallback });
    });
    await waitFor(() => expect(freshCallback).toHaveBeenCalledTimes(1));
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(body(fetch.mock.calls[0]![1]).verification_id).not.toBe(oldID);
    expect(body(fetch.mock.calls[0]![1]).email).toBe("same@example.test");
    expect(new Headers(fetch.mock.calls[0]![1]?.headers).get("X-Profile-Token")).toBe("fresh-pin");
    client.clear();
  },
);

it("recovers fresh same-email authority after uncertainty while retaining unchanged-authority retries", async () => {
  const { client, result, rerender } = harness();
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockRejectedValueOnce(new TypeError("first uncertain response"))
    .mockRejectedValueOnce(new TypeError("second uncertain response"))
    .mockImplementation(async (_url, options) => response(body(options).verification_id));
  vi.stubGlobal("fetch", fetch);
  for (let attempt = 0; attempt < 2; attempt++) {
    await act(async () => {
      await expect(result.current.mutateAsync("same@example.test")).rejects.toThrow();
    });
  }
  expect(fetch.mock.calls[0]![1]?.body).toBe(fetch.mock.calls[1]![1]?.body);
  setProfileToken("fresh-pin");
  rerender();
  await act(async () => {
    await result.current.mutateAsync("same@example.test");
  });
  expect(fetch).toHaveBeenCalledTimes(3);
  expect(body(fetch.mock.calls[2]![1]).verification_id).not.toBe(
    body(fetch.mock.calls[0]![1]).verification_id,
  );
  expect(new Headers(fetch.mock.calls[2]![1]?.headers).get("X-Profile-Token")).toBe("fresh-pin");
  client.clear();
});
