// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useRedetectItemMarkers } from "../items";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(installPolicyStorageMocks);
afterEach(() => vi.unstubAllGlobals());
function setup(response: () => Response) {
  const calls: string[] = [];
  const bodies: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push(new URL(String(input), "http://localhost").pathname);
      bodies.push(typeof init?.body === "string" ? JSON.parse(init.body) : init?.body);
      return response();
    }),
  );
  const client = new QueryClient({ defaultOptions: { mutations: { retry: 3 } } });
  return {
    calls,
    bodies,
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
  const { result } = renderHook(useRedetectItemMarkers, { wrapper });
  await act(async () => {
    await expect(
      result.current.mutateAsync({ itemId: "episode-1", kind: "all" }),
    ).rejects.toThrow();
  });
  expect(calls).toEqual(["/api/v2/admin/items/episode-1/redetect-markers"]);
});
it.each(["queued", "already_running"] as const)("preserves %s acknowledgment", async (status) => {
  const { calls, wrapper } = setup(() => jsonResponse({ status }, 202));
  const { result } = renderHook(useRedetectItemMarkers, { wrapper });
  await act(async () => {
    await expect(
      result.current.mutateAsync({ itemId: "episode-1", kind: "intro" }),
    ).resolves.toEqual({ status });
  });
  expect(calls).toHaveLength(1);
});
it.each(["intro", "credits", "all"] as const)("sends the %s kind", async (kind) => {
  const { calls, bodies, wrapper } = setup(() => jsonResponse({ status: "queued" }, 202));
  const { result } = renderHook(useRedetectItemMarkers, { wrapper });
  await act(async () => {
    await result.current.mutateAsync({ itemId: "movie-1", kind });
  });
  expect(calls).toEqual(["/api/v2/admin/items/movie-1/redetect-markers"]);
  expect(bodies).toEqual([{ kind }]);
});
