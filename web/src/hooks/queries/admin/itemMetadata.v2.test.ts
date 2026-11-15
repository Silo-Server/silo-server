// @vitest-environment jsdom
import { setAccessToken, setRefreshToken } from "@/api/client";
import { QueryClient, QueryClientProvider, useQuery, useQueryClient } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import queued from "../../../../../contracts/api/v2/fixtures/admin_item_refresh_queued.json";
import updated from "../../../../../contracts/api/v2/fixtures/admin_item_metadata_updated.json";
import {
  useApplyItemImage,
  useItemImageWritePending,
  useItemMetadataResetPending,
  useRefreshItemMetadata,
  useUpdateItemMetadata,
  useUploadItemImage,
} from "../items";
import { itemKeys } from "../keys";
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

// Edit Metadata closes when a save succeeds and builds a reopened form from the
// item, so the item must be refetched by then (#4241).
it("finishes a save only after the item has refetched", async () => {
  const { wrapper } = setup(() => jsonResponse(updated));
  let reads = 0;
  const { result } = renderHook(
    () => ({
      client: useQueryClient(),
      detail: useQuery({
        queryKey: itemKeys.detail("item-1"),
        queryFn: async () => {
          const read = ++reads;
          // A refetch slower than the save's own callbacks.
          if (read > 1) await new Promise((resolve) => setTimeout(resolve, 20));
          return { read };
        },
      }),
      update: useUpdateItemMetadata("item-1"),
    }),
    { wrapper },
  );
  await vi.waitFor(() => expect(result.current.detail.data?.read).toBe(1));
  let readOnSuccess: unknown;
  await act(async () => {
    await result.current.update.mutateAsync(
      { title: "After" },
      {
        onSuccess: () => {
          readOnSuccess = result.current.client.getQueryData(itemKeys.detail("item-1"));
        },
      },
    );
  });
  expect(readOnSuccess).toEqual({ read: 2 });
});

// Edit Metadata unmounts its image tab, and starts a new form, every time it
// closes. A reopened dialog must still see an image write or a reset that an
// earlier opening started, and only for its own item (#4243).
it.each([
  ["an image apply", "images"],
  ["an image upload", "images"],
  ["a reset", "reset"],
] as const)(
  "reports %s for the item after the hook that started it unmounts",
  async (write, reads) => {
    let finish!: () => void;
    const gate = new Promise<void>((resolve) => (finish = resolve));
    // A reset stays pending through its job; an image write through its request.
    if (reads === "reset") {
      awaitAdminJob.mockImplementationOnce(async (id: string) => {
        await gate;
        return { id, status: "completed", result_payload: {} };
      });
    }
    const { wrapper } = setup(() => jsonResponse(queued, 202));
    const fetchMock = vi.mocked(fetch);
    const respond = fetchMock.getMockImplementation()!;
    fetchMock.mockImplementation(async (input, init) => {
      if (String(input).includes("/images/")) await gate;
      return respond(input, init);
    });
    const writer = renderHook(
      () => ({
        apply: useApplyItemImage(),
        upload: useUploadItemImage(),
        refresh: useRefreshItemMetadata(),
      }),
      { wrapper },
    );
    const reader = renderHook(
      () => ({
        images: useItemImageWritePending("item-1"),
        reset: useItemMetadataResetPending("item-1"),
        otherImages: useItemImageWritePending("item-2"),
        otherReset: useItemMetadataResetPending("item-2"),
      }),
      { wrapper },
    );
    const item = { content_id: "item-1", type: "movie" as const };
    act(() => {
      if (write === "an image apply") {
        writer.result.current.apply.mutate({
          item,
          request: {
            original_url: "https://images.example/p.jpg",
            type: "poster",
            provider_id: "tmdb",
          },
        });
      } else if (write === "an image upload") {
        writer.result.current.upload.mutate({
          item,
          type: "poster",
          file: new File(["x"], "poster.jpg", { type: "image/jpeg" }),
        });
      } else {
        writer.result.current.refresh.mutate({ item, mode: "reset" });
      }
    });
    writer.unmount();

    await vi.waitFor(() => expect(reader.result.current[reads]).toBe(true));
    expect(reader.result.current).toEqual({
      images: reads === "images",
      reset: reads === "reset",
      otherImages: false,
      otherReset: false,
    });
    await act(async () => finish());
    await vi.waitFor(() => expect(reader.result.current[reads]).toBe(false));
  },
);

it("does not report a quick refresh as a reset", async () => {
  const { wrapper } = setup(() => jsonResponse(queued, 202));
  awaitAdminJob.mockImplementationOnce(() => new Promise(() => {}));
  const { result } = renderHook(
    () => ({ refresh: useRefreshItemMetadata(), reset: useItemMetadataResetPending("item-1") }),
    { wrapper },
  );
  act(() => {
    result.current.refresh.mutate({ item: { content_id: "item-1", type: "movie" }, mode: "quick" });
  });
  await vi.waitFor(() => expect(result.current.refresh.isPending).toBe(true));
  expect(result.current.reset).toBe(false);
});
