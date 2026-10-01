import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
} from "@/api/client";
import { useUpdateRateLimitConfig, type RateLimitIntent } from "./rateLimits";
import { toast } from "sonner";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
function wrapper() {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: 3 } } });
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
}
function intent(): RateLimitIntent {
  return {
    profileContext: captureProfileRequestContext()!,
    etag: '"captured"',
    config: {
      enabled: false,
      backend: "memory",
      global_requests_per_second: 100,
      ip_requests_per_second: 10,
      ip_requests_per_minute: 60,
      ip_burst: 5,
      tiers: {},
      auth_endpoints: {},
    },
  };
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken("refresh");
  setProfileId("profile-a");
  vi.clearAllMocks();
  onlineManager.setOnline(true);
});
afterEach(() => {
  onlineManager.setOnline(true);
  cleanup();
  vi.unstubAllGlobals();
});
it("sends the captured validator once on 401 without refresh or replay", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
    new Response(
      JSON.stringify({
        type: "https://siloserver.org/docs/api/v2/problems/authentication_required",
        title: "Unauthorized",
        status: 401,
      }),
      { status: 401, headers: { "Content-Type": "application/problem+json" } },
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useUpdateRateLimitConfig(), { wrapper: wrapper() });
  act(() => result.current.mutate(intent()));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/admin/rate-limits/config");
  expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
    method: "PATCH",
    headers: { "If-Match": '"captured"', "X-Profile-Id": "profile-a" },
  });
});
it("rejects offline queued intent after the selected profile changes", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useUpdateRateLimitConfig(), { wrapper: wrapper() });
  act(() => result.current.mutate(intent()));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => {
    setProfileId("profile-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
  expect(toast.error).not.toHaveBeenCalled();
});
it("does not rebase or replay a stale validator", async () => {
  const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
    new Response(
      JSON.stringify({
        type: "https://siloserver.org/docs/api/v2/problems/precondition_failed",
        title: "Changed",
        status: 412,
      }),
      { status: 412, headers: { "Content-Type": "application/problem+json", ETag: '"current"' } },
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useUpdateRateLimitConfig(), { wrapper: wrapper() });
  act(() => result.current.mutate(intent()));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(toast.error).toHaveBeenCalledWith(expect.stringContaining("Reload and review"));
});
