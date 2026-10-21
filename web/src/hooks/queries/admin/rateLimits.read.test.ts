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
