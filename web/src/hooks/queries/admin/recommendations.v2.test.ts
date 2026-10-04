// @vitest-environment jsdom
import { setAccessToken, setRefreshToken } from "@/api/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import {
  useRecommendationsStatus,
  useTriggerEmbeddings,
  useTriggerTasteProfiles,
  useTriggerCowatch,
  useTriggerRecommendations,
  useResetEmbeddings,
} from "./recommendations";

vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));
beforeEach(installPolicyStorageMocks);
afterEach(() => vi.unstubAllGlobals());
function setup(response: () => Response) {
  const calls: Array<{ path: string; method?: string }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({
        path: new URL(String(input), "http://localhost").pathname,
        method: init?.method,
      });
      return response();
    }),
  );
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  return {
    calls,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
const triggers = [
  ["embeddings", useTriggerEmbeddings],
  ["taste-profiles", useTriggerTasteProfiles],
  ["cowatch", useTriggerCowatch],
  ["recommendations", useTriggerRecommendations],
] as const;
it.each(triggers)("sends %s once after 401 without refresh or replay", async (suffix, hook) => {
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
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
  const { result } = renderHook(hook, { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync()).rejects.toThrow();
  });
  expect(calls).toEqual([
    { path: `/api/v2/admin/recommendations/trigger/${suffix}`, method: "POST" },
  ]);
});
it.each(triggers)("starts %s using the generated v2 operation", async (suffix, hook) => {
  const { calls, wrapper } = setup(() => jsonResponse({ status: "started" }));
  const { result } = renderHook(hook, { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync()).resolves.toEqual({ status: "started" });
  });
  expect(calls).toEqual([
    { path: `/api/v2/admin/recommendations/trigger/${suffix}`, method: "POST" },
  ]);
});
it("reads typed counts, last runs and running flags from v2", async () => {
  const status = {
    embeddings: {
      running: true,
      count: 3,
      total: 10,
      last_run: {
        status: "failed",
        started_at: "2026-10-03T03:00:00.000Z",
        completed_at: "2026-10-03T03:01:30.000Z",
        error: "embedding provider unavailable",
        result: { embedded: 0 },
      },
    },
    taste_profiles: { running: false, count: 4 },
    cowatch: { running: false, count: 6 },
    recommendations: { running: false, count: 5 },
    lock_conflict: "",
    cache_refreshed_at: "2026-10-03T05:01:00.000Z",
  };
  const { calls, wrapper } = setup(() => jsonResponse(status));
  const { result, unmount } = renderHook(useRecommendationsStatus, { wrapper });
  await waitFor(() => expect(result.current.data).toEqual(status));
  expect(calls[0]?.path).toBe("/api/v2/admin/recommendations/status");
  unmount();
});

it("resets embeddings once, without refresh or replay after 401", async () => {
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
  const { result } = renderHook(useResetEmbeddings, { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync()).rejects.toThrow();
  });
  expect(calls).toEqual([
    { path: "/api/v2/admin/recommendations/embeddings/reset", method: "POST" },
  ]);
});
it("resets embeddings using the generated v2 operation", async () => {
  const deleted = { embeddings: 120, taste_profiles: 3, taste_clusters: 9, cached_rows: 41 };
  const { calls, wrapper } = setup(() => jsonResponse(deleted));
  const { result } = renderHook(useResetEmbeddings, { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync()).resolves.toEqual(deleted);
  });
  expect(calls).toEqual([
    { path: "/api/v2/admin/recommendations/embeddings/reset", method: "POST" },
  ]);
});
