import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { renderHook, waitFor, act, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useUserIPs, useIPUsers } from "./ips";
function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
}
function response(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}
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
it("loads only requested pages, refetches cached pages without false cursor cycles, and restarts explicitly", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
    const cursor = new URL(String(input), "http://localhost").searchParams.get("cursor");
    return response({
      items: [{ client_ip: cursor ? "198.51.100.2" : "198.51.100.1", request_count: 1 }],
      page: cursor ? { has_more: false } : { has_more: true, next_cursor: "second" },
    });
  });
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useUserIPs(7), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.data).toHaveLength(1));
  expect(fetch).toHaveBeenCalledTimes(1);
  await act(async () => {
    await result.current.fetchNextPage();
  });
  await waitFor(() => expect(result.current.data).toHaveLength(2));
  await act(async () => {
    await result.current.refetch();
  });
  expect(result.current.isError).toBe(false);
  await waitFor(() => expect(result.current.data).toHaveLength(2));
  expect(fetch).toHaveBeenCalledTimes(4);
  await act(async () => {
    await result.current.restart();
  });
  await waitFor(() => expect(result.current.data).toHaveLength(1));
  expect(fetch).toHaveBeenCalledTimes(5);
});
it("keeps previous IP users visible on malformed continuation and never follows a cycle", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(
      response({
        items: [{ user_id: "7", username: "user", request_count: 1 }],
        page: { has_more: true, next_cursor: "second" },
      }),
    )
    .mockResolvedValueOnce(
      response({ items: [], page: { has_more: true, next_cursor: "second" } }),
    );
  vi.stubGlobal("fetch", fetch);
  const { result } = renderHook(() => useIPUsers("198.51.100.1"), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.data).toHaveLength(1));
  await act(async () => {
    await result.current.fetchNextPage();
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data?.[0]?.user_id).toBe(7);
  expect(fetch).toHaveBeenCalledTimes(2);
});

it("refuses an IP account ID that cannot be represented exactly by existing user links", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>().mockResolvedValue(
      response({
        items: [{ user_id: "9007199254740993", username: "user", request_count: 1 }],
        page: { has_more: false },
      }),
    ),
  );
  const { result } = renderHook(() => useIPUsers("198.51.100.1"), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data).toBeUndefined();
});
