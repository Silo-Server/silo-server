import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useRateLimitConfig } from "./rateLimits";
function wrapper(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
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
it("merges desired settings and process status through the actual v2 boundary", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockImplementation(
    async (url) =>
      new Response(
        JSON.stringify(
          String(url).endsWith("/status")
            ? { active: false, redis_available: true }
            : {
                enabled: true,
                backend: "redis",
                tiers: {},
                auth_endpoints: {},
                global_requests_per_second: 100,
                ip_requests_per_second: 10,
                ip_requests_per_minute: 60,
                ip_burst: 5,
              },
        ),
        { headers: { "Content-Type": "application/json", ETag: '"revision"' } },
      ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useRateLimitConfig(), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toMatchObject({
    enabled: true,
    backend: "redis",
    active: false,
    redis_available: true,
  });
  expect(fetchMock.mock.calls.map((c) => c[0])).toEqual([
    "/api/v2/admin/rate-limits/config",
    "/api/v2/admin/rate-limits/status",
  ]);
  expect(fetchMock.mock.calls[0]?.[1]?.headers).toMatchObject({ "X-Profile-Id": "profile-a" });
});
it("rejects the combined view when profile changes during body decoding", async () => {
  let finish!: (body: string) => void;
  let start!: () => void;
  const reading = new Promise<void>((resolve) => {
    start = resolve;
  });
  const response = new Response(null, {
    headers: { "Content-Type": "application/json", ETag: '"revision"' },
  });
  vi.spyOn(response, "text").mockImplementation(() => {
    start();
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>().mockImplementation(async (url) =>
      String(url).endsWith("/config")
        ? response
        : new Response(JSON.stringify({ active: false, redis_available: false }), {
            headers: { "Content-Type": "application/json", ETag: '"revision"' },
          }),
    ),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const { result } = renderHook(() => useRateLimitConfig(), { wrapper: wrapper(client) });
  await reading;
  const capturedQuery = client.getQueryCache().getAll()[0]!;
  await act(async () => {
    setProfileId("profile-b");
    finish(JSON.stringify({ tiers: {}, auth_endpoints: {} }));
  });
  await waitFor(() => expect(capturedQuery.state.status).toBe("error"));
  expect(capturedQuery.state.data).toBeUndefined();
  expect(result.current.data).toBeUndefined();
});
