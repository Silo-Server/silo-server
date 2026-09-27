import { useAdminCollectionsBoard } from "./collectionGroups";
import {
  useCollectionItems,
  useRemoveCollectionItem,
  useReorderCollectionItems,
} from "../collections";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { v2Problem } from "@/api/v2/problems.test-support";
import {
  useUpdateAdminCollection,
  useQueueCollectionTemplateBundleApply,
  useTemplateBundleApplyJobs,
} from "./collections";
const mocks = vi.hoisted(() => ({
  request: vi.fn(),
  api: vi.fn(),
  error: vi.fn(),
  invalidate: vi.fn(),
}));
vi.mock("@/api/v2/request", async () => ({
  ...(await vi.importActual<typeof import("@/api/v2/request")>("@/api/v2/request")),
  v2: mocks.request,
}));
vi.mock("@/api/client", async () => ({
  ...(await vi.importActual<typeof import("@/api/client")>("@/api/client")),
  api: mocks.api,
}));
vi.mock("../collectionSurfaceRefresh", () => ({
  invalidateAdminCollectionQueries: mocks.invalidate,
}));
vi.mock("sonner", () => ({ toast: { error: mocks.error, success: vi.fn(), warning: vi.fn() } }));
function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}
let client: QueryClient;
beforeEach(() => {
  vi.clearAllMocks();
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  mocks.api.mockResolvedValue({ jobs: [] });
});
describe("guarded admin lifecycle", () => {
  it("submits only the captured tag and preserves a 412 failure without retrying", async () => {
    mocks.request.mockRejectedValue(v2Problem(412, "precondition_failed", "Changed"));
    const { result } = renderHook(() => useUpdateAdminCollection(), { wrapper });
    await act(async () => {
      await expect(
        result.current.mutateAsync({
          id: "c",
          etag: '"captured"',
          body: { title: "My draft", poster_source_url: "https://example.test/image" },
        }),
      ).rejects.toMatchObject({ status: 412 });
    });
    expect(mocks.request.mock.calls).toEqual([
      [
        "PATCH /api/v2/admin/collections/{id}",
        {
          path: { id: "c" },
          headers: { "If-Match": '"captured"' },
          body: { title: "My draft", library_ids: undefined },
        },
      ],
    ]);
    expect(mocks.error).toHaveBeenCalledWith(expect.stringMatching(/Reload and review/));
    expect(mocks.invalidate).toHaveBeenCalled();
    await waitFor(() => expect(result.current.variables?.body.title).toBe("My draft"));
  });
  it("polls the accepted job through the dedicated endpoint and exposes its safe result", async () => {
    const job = {
      id: "job",
      kind: "template_bundle_apply",
      state: "succeeded",
      terminal: true,
      cancelable: false,
      created_at: "2026-09-05T00:00:00Z",
      template_result: {
        bundle_id: "bundle",
        dry_run: false,
        created: [],
        deleted: [],
        delete_skipped: [],
        delete_failed: [],
        skipped: [],
        failed: [],
        sync_queued: [],
        featured: [],
        featured_failed: [],
      },
    };
    mocks.request.mockImplementation(async (operation: string) =>
      operation === "GET /api/v2/admin/jobs"
        ? { items: [], page: { has_more: false } }
        : operation.startsWith("POST")
          ? { ...job, state: "queued", terminal: false }
          : job,
    );
    const { result } = renderHook(
      () => ({
        queue: useQueueCollectionTemplateBundleApply(),
        jobs: useTemplateBundleApplyJobs(),
      }),
      { wrapper },
    );
    await act(async () => {
      await result.current.queue.mutateAsync({ bundleId: "bundle", body: { library_ids: [7] } });
    });
    await waitFor(() => expect(result.current.jobs.data?.[0]?.status).toBe("completed"));
    expect(mocks.request).toHaveBeenCalledWith("GET /api/v2/admin/collection-jobs/{job_id}", {
      path: { job_id: "job" },
    });
    expect(result.current.jobs.data?.[0]?.result_payload).toMatchObject({ bundle_id: "bundle" });
    expect(
      mocks.request.mock.calls.filter(([operation]) => operation.startsWith("POST")),
    ).toHaveLength(1);
  });
  it("uses the bounded admin membership page and admin mutations for a library collection", async () => {
    mocks.request.mockResolvedValue({ items: [], page: { has_more: false } });
    const { result } = renderHook(
      () => ({
        items: useCollectionItems("c", "cursor", "library"),
        remove: useRemoveCollectionItem("c", "library"),
        reorder: useReorderCollectionItems("c", "library"),
      }),
      { wrapper },
    );
    await waitFor(() => expect(result.current.items.isSuccess).toBe(true));
    expect(mocks.request).toHaveBeenCalledWith("GET /api/v2/admin/collections/{id}/items", {
      path: { id: "c" },
      query: { limit: 200, cursor: "cursor" },
    });
    await act(async () => {
      await result.current.remove.mutateAsync("item");
      await result.current.reorder.mutateAsync({ orderedIds: ["item"], etag: '"order"' });
    });
    expect(mocks.request).toHaveBeenCalledWith(
      "DELETE /api/v2/admin/collections/{id}/items/{item_id}",
      { path: { id: "c", item_id: "item" } },
    );
    expect(mocks.request).toHaveBeenCalledWith("PUT /api/v2/admin/collections/{id}/items/order", {
      path: { id: "c" },
      headers: { "If-Match": '"order"' },
      body: { ordered_ids: ["item"] },
    });
    expect(
      mocks.request.mock.calls.some(([operation]) => operation.includes("/api/v2/collections/")),
    ).toBe(false);
  });
  it("matches canonical raw ID ordering when legacy rows share a position", async () => {
    mocks.request.mockImplementation(async (operation: string) =>
      operation === "GET /api/v2/admin/libraries/{library_id}/collection-groups"
        ? {
            items: [
              { id: "z", name: "A", library_id: "7", sort_order: 0 },
              { id: "A", name: "Z", library_id: "7", sort_order: 0 },
            ],
            ungrouped_sort_order: 0,
          }
        : {
            items: [
              {
                id: "z",
                title: "A",
                library_id: "7",
                library_ids: ["7"],
                sort_order: 0,
                group_id: null,
                query_definition: {},
              },
              {
                id: "A",
                title: "Z",
                library_id: "7",
                library_ids: ["7"],
                sort_order: 0,
                group_id: null,
                query_definition: {},
              },
            ],
            groups: [],
          },
    );
    const { result } = renderHook(() => useAdminCollectionsBoard(7), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.groups.map((group) => group.id)).toEqual(["A", "z"]);
    expect(result.current.data?.ungrouped.map((item) => item.id)).toEqual(["A", "z"]);
  });
});
