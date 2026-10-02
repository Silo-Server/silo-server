// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import imported from "../../../../../contracts/api/v2/fixtures/admin_catalog_import_committed.json";
import queued from "../../../../../contracts/api/v2/fixtures/admin_catalog_import_queued.json";
import status from "../../../../../contracts/api/v2/fixtures/admin_catalog_search_status.json";
import {
  useCreateCatalogExportJob,
  useImportCatalogSeed,
  usePublishCatalogExportJob,
} from "./libraries";
import { useCatalogSearchStatus } from "./settings";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(installPolicyStorageMocks);
afterEach(() => vi.unstubAllGlobals());
const source = {
  source: "local_path" as const,
  local_path: "/seed.json.gz",
  conflict_mode: "skip_existing" as const,
  path_rewrites: [],
};
function setup(response: () => Response) {
  const calls: Array<{ url: URL; init?: RequestInit }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      calls.push({ url: new URL(String(input), "http://localhost"), init });
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
function useTransfers() {
  return {
    exporter: useCreateCatalogExportJob(),
    importer: useImportCatalogSeed(),
    publisher: usePublishCatalogExportJob(),
  };
}
function problem(code: number) {
  return new Response(
    JSON.stringify({
      type: "https://siloserver.org/docs/api/v2/problems/unauthenticated",
      title: "Request refused",
      status: code,
      detail: "Request refused",
      instance: "urn:silo:request:test",
    }),
    { status: code, headers: { "Content-Type": "application/problem+json" } },
  );
}

it.each(["export", "queued", "synchronous", "publish"] as const)(
  "does not retry %s after authentication refusal",
  async (action) => {
    const { calls, wrapper } = setup(() => problem(401));
    const { result } = renderHook(useTransfers, { wrapper });
    await act(async () => {
      const pending =
        action === "export"
          ? result.current.exporter.mutateAsync({ library_ids: [1] })
          : action === "publish"
            ? result.current.publisher.mutateAsync("catalog-job")
            : result.current.importer.mutateAsync({ ...source, execution: action });
      await expect(pending).rejects.toThrow();
    });
    expect(calls).toHaveLength(1);
    expect(calls[0]?.url.pathname).toMatch(/^\/api\/v2\/admin\/catalog\//);
  },
);
it("does not silently switch a failed queued import to synchronous execution", async () => {
  const { calls, wrapper } = setup(() => problem(404));
  const { result } = renderHook(useImportCatalogSeed, { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync(source)).rejects.toThrow();
  });
  expect(calls).toHaveLength(1);
  expect(calls[0]?.url.pathname).toBe("/api/v2/admin/catalog/import-jobs");
});
it.each(["queued", "synchronous"] as const)(
  "uses the explicit %s import contract",
  async (execution) => {
    const { calls, wrapper } = setup(() =>
      jsonResponse(execution === "queued" ? queued : imported, execution === "queued" ? 202 : 200),
    );
    const { result } = renderHook(useImportCatalogSeed, { wrapper });
    let outcome: Awaited<ReturnType<typeof result.current.mutateAsync>> | undefined;
    await act(async () => {
      outcome = await result.current.mutateAsync({ ...source, execution });
    });
    expect(calls[0]?.url.pathname).toBe(
      `/api/v2/admin/catalog/${execution === "queued" ? "import-jobs" : "import"}`,
    );
    expect(JSON.parse(String(calls[0]?.init?.body))).toEqual({
      local_path: "/seed.json.gz",
      conflict_mode: "skip_existing",
      path_rewrites: [],
    });
    expect(outcome?.mode).toBe(execution === "queued" ? "job" : "sync");
  },
);
it("keeps large catalog search event identities as strings", async () => {
  const { wrapper } = setup(() => jsonResponse(status));
  const { result } = renderHook(() => useCatalogSearchStatus(), { wrapper });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data?.index.last_processed_event_id).toBe("9007199254740993");
});
