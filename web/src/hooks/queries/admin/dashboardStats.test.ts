import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
} from "@/api/client";
import { adminStatsKey, fetchAdminStats, useAdminStats } from "./stats";
function wrapper(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken(null);
  setProfileId("profile-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("reads dashboard aggregates through v2", async () => {
  const body = { total_users: 3, watch_providers: [] };
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValue(
      new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useAdminStats(), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toMatchObject(body);
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/v2/admin/stats");
});
it("rejects metadata decoded under a different account or profile", async () => {
  let finish!: (body: string) => void;
  let start!: () => void;
  const reading = new Promise<void>((resolve) => {
    start = resolve;
  });
  const response = new Response(null, { headers: { "Content-Type": "application/json" } });
  vi.spyOn(response, "text").mockImplementation(() => {
    start();
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderHook(() => useAdminStats(), { wrapper: wrapper(client) });
  await reading;
  const captured = client.getQueryCache().getAll()[0]!;
  await act(async () => {
    setProfileId("profile-b");
    finish(JSON.stringify({ status: "available" }));
  });
  await waitFor(() => expect(captured.state.status).toBe("error"));
  expect(captured.state.data).toBeUndefined();
});

it("publishes manual refresh into the mounted authority-scoped query", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  let count = 1;
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation(
      async () =>
        new Response(JSON.stringify({ total_users: count++, watch_providers: [] }), {
          headers: { "Content-Type": "application/json" },
        }),
    ),
  );
  const { result } = renderHook(() => useAdminStats(), { wrapper: wrapper(client) });
  await waitFor(() => expect(result.current.data?.total_users).toBe(1));
  const profileContext = captureProfileRequestContext()!;
  await act(async () => {
    const next = await fetchAdminStats({ refresh: true, profileContext });
    client.setQueryData(adminStatsKey(profileContext), next);
  });
  await waitFor(() => expect(result.current.data?.total_users).toBe(2));
});
