// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useRedetectEpisodeIntro } from "../items";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(installPolicyStorageMocks);
afterEach(() => vi.unstubAllGlobals());
function setup(response: () => Response) {
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      calls.push(new URL(String(input), "http://localhost").pathname);
      return response();
    }),
  );
  const client = new QueryClient({ defaultOptions: { mutations: { retry: 3 } } });
  return {
    calls,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
it("does not refresh or replay re-detection after401", async () => {
  const { calls, wrapper } = setup(
    () =>
      new Response(
        JSON.stringify({
          type: "https://siloserver.org/docs/api/v2/problems/invalid_token",
          title: "Invalid token",
          status: 401,
          detail: "Expired",
          instance: "urn:silo:request:test",
        }),
        { status: 401, headers: { "Content-Type": "application/problem+json" } },
      ),
  );
  const { result } = renderHook(useRedetectEpisodeIntro, { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync("episode-1")).rejects.toThrow();
  });
  expect(calls).toEqual(["/api/v2/admin/items/episode-1/redetect-intro"]);
});
it.each(["queued", "already_running"] as const)("preserves %s acknowledgment", async (status) => {
  const { calls, wrapper } = setup(() => jsonResponse({ status }, 202));
  const { result } = renderHook(useRedetectEpisodeIntro, { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync("episode-1")).resolves.toEqual({ status });
  });
  expect(calls).toHaveLength(1);
});
