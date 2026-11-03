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
