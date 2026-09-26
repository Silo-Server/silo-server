// @vitest-environment jsdom
import { fetchAdminTaskJob } from "@/api/v2/adminTasks";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, cleanup, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useRunTask, useCancelTask, useUpdateTriggers, useTaskHistory } from "./tasks";
import { useAdminTaskJobs } from "./taskJobs";

beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("old-access");
  setRefreshToken("refresh-token");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { mutations: { retry: 2 }, queries: { retry: false } } })
      }
    >
      {children}
    </QueryClientProvider>
  );
}
it("does not replay any task mutation on 401 and preserves the submitted guard", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () =>
    jsonResponse(
      {
        type: "https://siloserver.org/docs/api/v2/problems/authentication_required",
        title: "Authentication required",
        status: 401,
        detail: "Sign in again",
      },
      401,
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () => ({ run: useRunTask(), cancel: useCancelTask(), update: useUpdateTriggers() }),
    { wrapper },
  );
  for (const call of [
    () => result.current.run.mutateAsync("fixture"),
    () => result.current.cancel.mutateAsync("fixture"),
    () => result.current.update.mutateAsync({ key: "fixture", triggers: [], etag: '"original"' }),
  ]) {
    const before = fetchMock.mock.calls.length;
    await act(async () => {
      await expect(call()).rejects.toMatchObject({ status: 401 });
    });
    expect(fetchMock).toHaveBeenCalledTimes(before + 1);
  }
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("/auth/refresh"))).toBe(false);
  expect(new Headers(fetchMock.mock.calls[2]?.[1]?.headers).get("If-Match")).toBe('"original"');
});
it("loads task history only through explicit continuation", async () => {
  const fetchMock = vi.fn<typeof fetch>(async (url) =>
    jsonResponse({
      items: [{ id: String(url).includes("cursor=") ? "older" : "newer" }],
      page: String(url).includes("cursor=")
        ? { has_more: false }
        : { has_more: true, next_cursor: "history-cursor" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useTaskHistory("fixture"), { wrapper });
  await waitFor(() => expect(result.current.data?.[0]?.id).toBe("newer"));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  await act(async () => {
    await result.current.fetchNextPage();
  });
  await waitFor(() => expect(result.current.data?.map((e) => e.id)).toEqual(["newer", "older"]));
  expect(result.current.hasNextPage).toBe(false);
});
it("loads job pages with the same kind and preserves opaque library identities", async () => {
  const fetchMock = vi.fn<typeof fetch>(async (url) =>
    jsonResponse({
      items: [
        {
          id: String(url).includes("cursor=") ? "older" : "newer",
          kind: "library_refresh",
          state: "running",
          created_at: "2026-09-05T00:00:00.000Z",
          library_ids: [],
          library_id: "9007199254740993",
          artifact_size_bytes: 0,
        },
      ],
      page: String(url).includes("cursor=")
        ? { has_more: false }
        : { has_more: true, next_cursor: "jobs-cursor" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useAdminTaskJobs("library_refresh", 2), { wrapper });
  await waitFor(() => expect(result.current.data?.length).toBe(1));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  await act(async () => {
    await result.current.fetchNextPage();
  });
  await waitFor(() => expect(result.current.data?.length).toBe(2));
  expect(result.current.data?.[0]?.request_payload).toMatchObject({
    library_id: "9007199254740993",
  });
  expect(String(fetchMock.mock.calls[1]?.[0])).toContain("kind=library_refresh");
});

it("keeps item refresh completion warnings and new-file counts for the existing item consumer", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async () =>
      jsonResponse({
        id: "item-job",
        kind: "item_refresh",
        state: "succeeded",
        created_at: "2026-09-05T00:00:00.000Z",
        library_ids: [],
        artifact_size_bytes: 0,
        item_result: {
          requested_content_id: "item",
          refresh_content_id: "item",
          detail_content_id: "detail",
          new_files: 3,
          artwork_cache_incomplete: true,
          matched_files: 4,
        },
      }),
    ),
  );
  const job = await fetchAdminTaskJob("item-job");
  expect(job.result_payload).toMatchObject({
    detail_content_id: "detail",
    scan_result: { new: 3 },
    artwork_cache_warning: expect.stringContaining("Artwork caching did not finish"),
  });
});

it("keeps structured storage transition progress and failure categories", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async () =>
      jsonResponse({
        id: "storage-job",
        kind: "storage_transition",
        state: "failed",
        created_at: "2026-09-05T00:00:00.000Z",
        library_ids: [],
        artifact_size_bytes: 0,
        failure: {
          detail: "private/object-key",
          retryable: true,
          title: "Failed",
          type: "failure",
        },
        storage_transition_result: {
          manual_restart_required: false,
          phase: "failed",
          verified_objects: 4,
          failure_category: "copy_failed",
        },
      }),
    ),
  );

  const job = await fetchAdminTaskJob("storage-job");
  expect(job.result_payload).toMatchObject({
    phase: "failed",
    verified_objects: 4,
    failure_category: "copy_failed",
  });
  expect(job.error_message).toBeUndefined();
});
