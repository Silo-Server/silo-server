// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useTranslateItemMetadata, useMetadataTranslationJobs } from "../items";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(installPolicyStorageMocks);
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
it("does not refresh or retry translation enqueue after401", async () => {
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
  const { result } = renderHook(() => useTranslateItemMetadata("item-1"), { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync({ target_language: "de" })).rejects.toThrow();
  });
  expect(calls).toHaveLength(1);
  expect(calls[0]?.path).toBe("/api/v2/admin/items/item-1/metadata-translation");
});
it("retains opaque job identity and explicit enqueue options", async () => {
  const job = { id: "9007199254740993", status: "pending" };
  const { calls, wrapper } = setup(() => jsonResponse(job, 202));
  const { result } = renderHook(() => useTranslateItemMetadata("item-1"), { wrapper });
  await act(async () => {
    await expect(
      result.current.mutateAsync({ target_language: "de", include_children: false, force: true }),
    ).resolves.toEqual({ job });
  });
  expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({
    target_language: "de",
    include_children: false,
    force: true,
  });
});
it("reads the bounded recent job list through v2", async () => {
  const job = { id: "9007199254740993", status: "completed" };
  const { calls, wrapper } = setup(() => jsonResponse({ jobs: [job] }));
  const { result, unmount } = renderHook(() => useMetadataTranslationJobs("item-1", true), {
    wrapper,
  });
  await waitFor(() => expect(result.current.data?.jobs[0]?.id).toBe(job.id));
  expect(calls[0]?.path).toBe("/api/v2/admin/items/item-1/metadata-translation/jobs");
  unmount();
});
