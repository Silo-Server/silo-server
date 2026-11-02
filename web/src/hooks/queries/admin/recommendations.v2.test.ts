// @vitest-environment jsdom
import { setAccessToken, setRefreshToken } from "@/api/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks } from "@/pages/admin-policy/policyTestUtils";
import {
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
const writes = [
  ["trigger/embeddings", useTriggerEmbeddings],
  ["trigger/taste-profiles", useTriggerTasteProfiles],
  ["trigger/cowatch", useTriggerCowatch],
  ["trigger/recommendations", useTriggerRecommendations],
  ["embeddings/reset", useResetEmbeddings],
] as const;
it.each(writes)("sends %s once after 401 without refresh or replay", async (route, hook) => {
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
  const { result } = renderHook(() => hook(), { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync()).rejects.toThrow();
  });
  expect(calls).toEqual([{ path: `/api/v2/admin/recommendations/${route}`, method: "POST" }]);
});
