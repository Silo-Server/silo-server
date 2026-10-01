import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useAdminServerStatus } from "./settings";
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
it("reads restart counters and unhealthy dependency status through v2", async () => {
  const body = {
    started_at: "2026-09-01T00:00:00.000Z",
    restart_required: true,
    restart_required_reasons: ["setting:server.listen"],
    restart_mark_count: 2,
    restart_requested: false,
    health: {
      postgres: { configured: true, ok: false },
      redis: { configured: false },
      errors_24h: 4,
      warnings_24h: 0,
    },
  };
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValue(
      new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useAdminServerStatus(), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toEqual(body);
  expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/admin/server/status");
  expect(fetchMock.mock.calls[0]?.[1]?.headers).toMatchObject({
    "X-Profile-Id": "profile-a",
    "X-Profile-Token": "pin-a",
  });
});
it("rejects status decoded after the selected profile changes", async () => {
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
  const { result } = renderHook(() => useAdminServerStatus(), { wrapper: wrapper() });
  await reading;
  await act(async () => {
    setProfileId("profile-b");
    finish("{}");
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data).toBeUndefined();
});
