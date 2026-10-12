import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, expect, it, vi } from "vitest";

import { useCatalogItemDetailView, usePrefetchCatalogItemDetail } from "./catalogRead";
import { catalogKeys, personKeys } from "./keys";

const reads = vi.hoisted(
  () => [] as { id: string; query: Record<string, unknown> | undefined; fail: boolean }[],
);
const failViews = vi.hoisted(() => ({ value: false }));
const heldReads = vi.hoisted(() => ({ until: null as Promise<void> | null }));

vi.mock("@/api/v2/request", () => ({
  v2: async (_route: string, init: { path: { id: string }; query?: Record<string, unknown> }) => {
    if (heldReads.until) await heldReads.until;
    const fail = failViews.value && !init.query?.prefetch;
    reads.push({ id: init.path.id, query: init.query, fail });
    if (fail) throw new Error("view read failed");
    return { content_id: init.path.id, read: reads.length };
  },
}));

vi.mock("@/api/v2/catalog", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/v2/catalog")>()),
  catalogItemDetailFromV2: (detail: unknown) => ({ ...(detail as object) }),
}));

function client(itemDetailPrefetch: boolean) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { staleTime: 2 * 60_000, retry: false } },
  });
  queryClient.setQueryData(personKeys.searchCapabilities(), {
    state: "available",
    item_detail_prefetch: itemDetailPrefetch,
  });
  return queryClient;
}

function wrapperFor(queryClient: QueryClient) {
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  );
}

beforeEach(() => {
  reads.length = 0;
  failViews.value = false;
  heldReads.until = null;
});

it("marks a prefetch read so the server treats it as speculative", async () => {
  const queryClient = client(true);
  const { result } = renderHook(() => usePrefetchCatalogItemDetail(3), {
    wrapper: wrapperFor(queryClient),
  });

  act(() => result.current("movie-1"));

  await waitFor(() =>
    expect(queryClient.getQueryData(catalogKeys.itemDetail("movie-1", 3))).toBeDefined(),
  );
  expect(reads).toEqual([
    { id: "movie-1", query: { library_id: "3", prefetch: true }, fail: false },
  ]);
});

it("prefetches nothing from a server that does not advertise item_detail_prefetch", async () => {
  const queryClient = client(false);
  const { result } = renderHook(() => usePrefetchCatalogItemDetail(), {
    wrapper: wrapperFor(queryClient),
  });

  act(() => result.current("movie-1"));
  await act(async () => {});

  expect(reads).toEqual([]);
});

it("renders a prefetched detail at once, then reads it once as a view", async () => {
  const queryClient = client(true);
  const wrapper = wrapperFor(queryClient);
  const prefetch = renderHook(() => usePrefetchCatalogItemDetail(), { wrapper });
  act(() => prefetch.result.current("movie-1"));
  await waitFor(() =>
    expect(queryClient.getQueryData(catalogKeys.itemDetail("movie-1"))).toBeDefined(),
  );

  const view = renderHook(() => useCatalogItemDetailView("movie-1"), { wrapper });

  expect(view.result.current.data).toMatchObject({ content_id: "movie-1" });
  await waitFor(() => expect(reads).toHaveLength(2));
  expect(reads[1]).toEqual({ id: "movie-1", query: {}, fail: false });
  await act(async () => {});
  expect(reads).toHaveLength(2);
});

it("does not retry a failed view read in a loop", async () => {
  const queryClient = client(true);
  const wrapper = wrapperFor(queryClient);
  const prefetch = renderHook(() => usePrefetchCatalogItemDetail(), { wrapper });
  act(() => prefetch.result.current("movie-1"));
  await waitFor(() =>
    expect(queryClient.getQueryData(catalogKeys.itemDetail("movie-1"))).toBeDefined(),
  );
  failViews.value = true;

  const view = renderHook(() => useCatalogItemDetailView("movie-1"), { wrapper });

  await waitFor(() => expect(view.result.current.isFetching).toBe(false));
  await waitFor(() => expect(reads.filter((read) => read.fail)).toHaveLength(1));
  await act(async () => {});
  expect(reads).toHaveLength(2);
  expect(view.result.current.data).toMatchObject({ content_id: "movie-1" });
});

it("discards a prefetch read that a write overlapped", async () => {
  const queryClient = client(true);
  const { result } = renderHook(() => usePrefetchCatalogItemDetail(), {
    wrapper: wrapperFor(queryClient),
  });
  let finishWrite = () => {};
  const write = queryClient.getMutationCache().build(queryClient, {
    mutationFn: () => new Promise<void>((resolve) => (finishWrite = resolve)),
  });

  let releaseRead = () => {};
  heldReads.until = new Promise<void>((resolve) => (releaseRead = resolve));

  act(() => result.current("movie-1"));
  await act(async () => {});
  const writing = write.execute(undefined);
  releaseRead();
  heldReads.until = null;
  await waitFor(() =>
    expect(queryClient.getQueryState(catalogKeys.itemDetail("movie-1"))?.status).toBe("error"),
  );
  expect(queryClient.getQueryData(catalogKeys.itemDetail("movie-1"))).toBeUndefined();

  act(() => result.current("movie-2"));
  await act(async () => {});
  expect(reads.map((read) => read.id)).toEqual(["movie-1"]);

  finishWrite();
  await writing;
});
