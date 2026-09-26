import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useAdminTimeseries, useAdminDownloadsStats } from "./dashboardInsights";
function wrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("reads download account IDs as strings through the v2 session boundary", async () => {
  const body = {
    users_with_downloads: 1,
    active_downloads: 2,
    total_bytes: 3,
    downloads_started_24h: 4,
    downloads_completed_24h: 5,
    limit: 10,
    top_users: [
      { user_id: "9007199254740993", username: "test-user", downloads: 2, total_bytes: 3 },
    ],
  };
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValue(
      new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useAdminDownloadsStats(), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toEqual(body);
  expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/admin/stats/downloads?limit=10");
  expect(fetchMock.mock.calls[0]?.[1]?.headers).toMatchObject({
    "X-Profile-Id": "profile-a",
    "X-Profile-Token": "pin-a",
  });
});
it("discards statistics decoded after a profile switch", async () => {
  let finish!: (value: string) => void;
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
  const { result } = renderHook(() => useAdminTimeseries(), { wrapper: wrapper() });
  await reading;
  await act(async () => {
    setProfileId("profile-b");
    finish(JSON.stringify({ points: [] }));
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data).toBeUndefined();
});
