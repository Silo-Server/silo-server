import { setAccessToken, setRefreshToken, setProfileId } from "@/api/client";
// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useSearchItemMatchCandidates, useApplyItemMatch } from "../items";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("test-account");
  setRefreshToken("test-refresh");
  setProfileId("p-owner");
});
afterEach(() => vi.unstubAllGlobals());
function setup(response: () => Response) {
  const calls: Array<{ path: string; init?: RequestInit }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ path: new URL(String(input), "http://localhost").pathname, init });
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
it("adapts library identity and explicit truncation", async () => {
  const { calls, wrapper } = setup(() =>
    jsonResponse({
      candidates: [
        {
          title: "choice",
          year: 2026,
          content_type: "movie",
          provider_ids: { tmdb: "42" },
          sources: [],
          agreement_hints: [],
        },
      ],
      truncated: true,
    }),
  );
  const { result } = renderHook(() => useSearchItemMatchCandidates("item-1"), { wrapper });
  await act(async () => {
    const data = await result.current.mutateAsync({ title: "query", library_id: 7 });
    expect(data.truncated).toBe(true);
    expect(data.candidates[0]?.image_url).toBe("");
  });
  expect(calls[0]?.path).toBe("/api/v2/admin/items/item-1/match/search");
  expect(JSON.parse(String(calls[0]?.init?.body))).toMatchObject({ library_id: "7", limit: 500 });
});
for (const mode of ["search", "apply"] as const)
  it(`does not refresh or replay ${mode} after401`, async () => {
    const { calls, wrapper } = setup(
      () =>
        new Response(
          JSON.stringify({
            type: "https://siloserver.org/docs/api/v2/problems/authentication_required",
            title: "Unauthorized",
            status: 401,
          }),
          { status: 401, headers: { "Content-Type": "application/problem+json" } },
        ),
    );
    const { result } = renderHook(
      () => ({ search: useSearchItemMatchCandidates("item-1"), apply: useApplyItemMatch() }),
      { wrapper },
    );
    await act(async () => {
      const request =
        mode === "search"
          ? result.current.search.mutateAsync({ title: "query" })
          : result.current.apply.mutateAsync({
              item: { content_id: "item-1", type: "movie" },
              providerIds: { tmdb: "42" },
            });
      await expect(request).rejects.toThrow();
    });
    expect(calls).toHaveLength(1);
  });
