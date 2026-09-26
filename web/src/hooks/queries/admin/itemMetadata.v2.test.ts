// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import queued from "../../../../../contracts/api/v2/fixtures/admin_item_refresh_queued.json";
import updated from "../../../../../contracts/api/v2/fixtures/admin_item_metadata_updated.json";
import { useRefreshItemMetadata, useUpdateItemMetadata } from "../items";
const { awaitAdminJob } = vi.hoisted(() => ({
  awaitAdminJob: vi.fn(async (id: string) => ({ id, status: "completed", result_payload: {} })),
}));
vi.mock("@/components/realtimeEventsContext", () => ({
  useRealtimeEvents: () => ({ awaitAdminJob }),
}));
vi.mock("sonner", () => ({
  toast: { loading: vi.fn(() => "toast"), success: vi.fn(), error: vi.fn() },
}));
beforeEach(() => {
  installPolicyStorageMocks();
  awaitAdminJob.mockClear();
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
it.each(["refresh", "update"] as const)(
  "does not refresh or replay item %s after401",
  async (action) => {
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
    const { result } = renderHook(
      () => ({ refresh: useRefreshItemMetadata(), update: useUpdateItemMetadata("item-1") }),
      { wrapper },
    );
    await act(async () => {
      await expect(
        action === "refresh"
          ? result.current.refresh.mutateAsync({
              item: { content_id: "item-1", type: "movie" },
              mode: "quick",
            })
          : result.current.update.mutateAsync({ title: "Updated" }),
      ).rejects.toThrow();
    });
    expect(calls).toHaveLength(1);
    expect(awaitAdminJob).not.toHaveBeenCalled();
  },
);
it("waits on the returned persisted job without resubmitting refresh", async () => {
  const { calls, wrapper } = setup(() => jsonResponse(queued, 202));
  const { result } = renderHook(useRefreshItemMetadata, { wrapper });
  await act(async () => {
    await result.current.mutateAsync({
      item: { content_id: "item-1", type: "movie" },
      mode: "complete",
    });
  });
  expect(calls).toHaveLength(1);
  expect(calls[0]?.path).toBe("/api/v2/admin/items/item-1/refresh-metadata");
  expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({ mode: "complete" });
  expect(awaitAdminJob).toHaveBeenCalledWith("item-refresh-1");
});
it("preserves explicit clears and adapts canonical detail", async () => {
  const { calls, wrapper } = setup(() => jsonResponse(updated));
  const { result } = renderHook(() => useUpdateItemMetadata("item-1"), { wrapper });
  await act(async () => {
    expect((await result.current.mutateAsync({ overview: "", genres: [], year: 0 })).title).toBe(
      "Updated",
    );
  });
  expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({ overview: "", genres: [], year: 0 });
});
