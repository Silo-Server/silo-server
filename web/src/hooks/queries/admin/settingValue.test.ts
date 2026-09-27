import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId } from "@/api/client";
import { useAdminSettingValue } from "./settings";
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
it("reads a setting and preserves not-found as absence", async () => {
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({ key: "redis.url", value: "configured", restart_required: true }),
        { headers: { "Content-Type": "application/json" } },
      ),
    )
    .mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          type: "https://siloserver.org/docs/api/v2/problems/not_found",
          title: "Not found",
          status: 404,
        }),
        { status: 404, headers: { "Content-Type": "application/problem+json" } },
      ),
    );
  vi.stubGlobal("fetch", fetchMock);
  const first = renderHook(() => useAdminSettingValue("redis.url"), { wrapper: wrapper() });
  await waitFor(() => expect(first.result.current.isSuccess).toBe(true));
  expect(first.result.current.data).toBe("configured");
  const second = renderHook(() => useAdminSettingValue("missing"), { wrapper: wrapper() });
  await waitFor(() => expect(second.result.current.isSuccess).toBe(true));
  expect(second.result.current.data).toBeNull();
  expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/admin/settings/redis.url");
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
  renderHook(() => useAdminSettingValue("redis.url"), { wrapper: wrapper(client) });
  await reading;
  const captured = client.getQueryCache().getAll()[0]!;
  await act(async () => {
    setProfileId("profile-b");
    finish(JSON.stringify({ status: "available" }));
  });
  await waitFor(() => expect(captured.state.status).toBe("error"));
  expect(captured.state.data).toBeUndefined();
});
