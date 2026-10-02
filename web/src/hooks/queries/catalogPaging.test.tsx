import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import getCatalogFiltersOk from "../../../../contracts/api/v2/fixtures/get_catalog_filters_ok.json";
import { setProfileId } from "@/api/client";
import type { V2Body } from "@/api/v2/request";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { createCatalogSearchState, useCatalogFilters, useCatalogWindow } from "./catalog";

type CatalogQueryBody = V2Body<"POST /api/v2/catalog/query">;
interface RequestRecord {
  body: CatalogQueryBody;
  signal: AbortSignal | null | undefined;
}

function catalogTransport() {
  const requests: RequestRecord[] = [];
  const generations = new Map<string, number>();
  function reply(body: CatalogQueryBody) {
    const scope = JSON.stringify([
      body.q,
      body.library_id,
      body.type,
      body.groups,
      body.sort,
      body.order,
    ]);
    const generation = (generations.get(scope) ?? 0) + 1;
    if (!body.cursor) generations.set(scope, generation);
    const root = body.cursor?.replace(/^next:\d+:/, "") ?? `root:${scope}:${generation}`;
    const offset = body.seek ?? Number(body.cursor?.match(/^next:(\d+):/)?.[1] ?? 0);
    const limit = body.limit ?? 2;
    return jsonResponse({
      window_cursor: root,
      items: Array.from({ length: limit }, (_, index) => ({
        content_id: `item-${offset + index}`,
        title: `${body.q ?? "Item"} ${offset + index}`,
        type: "movie",
        status: "matched",
        genres: [],
        keywords: [],
      })),
      total: 0,
      total_exact: false,
      page: { has_more: true, next_cursor: `next:${offset + limit}:${root}` },
    });
  }
  const fetchMock = vi.fn<typeof fetch>(async (_url, options) => {
    const body = JSON.parse(String(options?.body)) as CatalogQueryBody;
    requests.push({ body, signal: options?.signal });
    return reply(body);
  });
  vi.stubGlobal("fetch", fetchMock);
  return { requests, fetchMock, reply };
}

describe("catalog window cursor paging", () => {
  const clients: QueryClient[] = [];
  function makeClient() {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    clients.push(queryClient);
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    );
    return { queryClient, wrapper };
  }

  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });
  afterEach(() => {
    cleanup();
    clients.splice(0).forEach((client) => client.clear());
    vi.unstubAllGlobals();
  });

  it("uses adjacent cached cursors and returns consecutive unique results", async () => {
    const { requests } = catalogTransport();
    const { wrapper } = makeClient();
    const state = createCatalogSearchState("query", { q: "star" });
    const { result, rerender } = renderHook(
      ({ visibleRange }) =>
        useCatalogWindow(state, { limit: 2, includeTotal: false, visibleRange }),
      { wrapper, initialProps: { visibleRange: [0, 1] as [number, number] } },
    );
    await waitFor(() => expect(result.current.data.pages.get(0)).toHaveLength(2));
    const ids = result.current.data.pages.get(0)!.map((item) => item.content_id);
    rerender({ visibleRange: [2, 3] });
    await waitFor(() => expect(result.current.data.pages.get(1)).toHaveLength(2));
    ids.push(...result.current.data.pages.get(1)!.map((item) => item.content_id));
    rerender({ visibleRange: [4, 5] });
    await waitFor(() => expect(result.current.data.pages.get(2)).toHaveLength(2));
    ids.push(...result.current.data.pages.get(2)!.map((item) => item.content_id));

    expect(requests).toHaveLength(3);
    expect(requests[1]?.body.cursor).toMatch(/^next:2:/);
    expect(requests[2]?.body.cursor).toMatch(/^next:4:/);
    expect(requests.every(({ body }) => body.seek === undefined)).toBe(true);
    expect(ids).toEqual(Array.from({ length: 6 }, (_, index) => `item-${index}`));
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("starts uncached distant windows in parallel and continues from the final loaded page", async () => {
    const { requests, fetchMock, reply } = catalogTransport();
    const pending: (() => void)[] = [];
    fetchMock.mockImplementation(async (_url, options) => {
      const body = JSON.parse(String(options?.body)) as CatalogQueryBody;
      requests.push({ body, signal: options?.signal });
      if (!body.seek) return reply(body);
      return new Promise((resolve) => pending.push(() => resolve(reply(body))));
    });
    const { wrapper } = makeClient();
    const state = createCatalogSearchState("query", { q: "star" });
    const { result, rerender } = renderHook(
      ({ visibleRange }) =>
        useCatalogWindow(state, { limit: 2, includeTotal: false, visibleRange }),
      { wrapper, initialProps: { visibleRange: [100, 105] as [number, number] } },
    );
    await waitFor(() => expect(pending).toHaveLength(3));
    expect(requests.slice(1).map(({ body }) => body.seek)).toEqual([100, 102, 104]);
    expect(requests.slice(1).every(({ body }) => body.cursor?.startsWith("root:"))).toBe(true);
    await act(async () => pending.forEach((finish) => finish()));
    await waitFor(() => expect(result.current.data.pages.get(52)).toHaveLength(2));
    expect(
      [50, 51, 52].flatMap((page) =>
        result.current.data.pages.get(page)!.map((item) => item.content_id),
      ),
    ).toEqual(["item-100", "item-101", "item-102", "item-103", "item-104", "item-105"]);

    rerender({ visibleRange: [106, 107] });
    await waitFor(() => expect(result.current.data.pages.get(53)).toHaveLength(2));
    expect(requests).toHaveLength(5);
    expect(requests[4]?.body.cursor).toMatch(/^next:106:/);
    expect(requests[4]?.body.seek).toBeUndefined();
  });

  it("seeks when the adjacent cached page has been invalidated", async () => {
    const { requests } = catalogTransport();
    const { wrapper, queryClient } = makeClient();
    const state = createCatalogSearchState("query", { q: "star" });
    const { result, rerender } = renderHook(
      ({ visibleRange }) =>
        useCatalogWindow(state, { limit: 2, includeTotal: false, visibleRange }),
      { wrapper, initialProps: { visibleRange: [2, 3] as [number, number] } },
    );
    await waitFor(() => expect(result.current.data.pages.get(1)).toHaveLength(2));
    await queryClient.invalidateQueries({
      predicate: (query) => (query.queryKey[2] as { offset?: number })?.offset === 2,
      refetchType: "none",
    });
    rerender({ visibleRange: [4, 5] });
    await waitFor(() => expect(result.current.data.pages.get(2)).toHaveLength(2));
    expect(requests.at(-1)?.body).toMatchObject({ seek: 4 });
    expect(requests.at(-1)?.body.cursor).toMatch(/^root:/);
  });

  it("discards cached page boundaries after refreshing the root traversal", async () => {
    const { requests } = catalogTransport();
    const { wrapper } = makeClient();
    const state = createCatalogSearchState("query", { q: "star" });
    const { result, rerender } = renderHook(
      ({ visibleRange }) =>
        useCatalogWindow(state, { limit: 2, includeTotal: false, visibleRange }),
      { wrapper, initialProps: { visibleRange: [2, 3] as [number, number] } },
    );
    await waitFor(() => expect(result.current.data.pages.get(1)).toHaveLength(2));
    const oldCursor = requests[1]!.body.cursor;
    await act(async () => result.current.refetch());
    await waitFor(() => expect(requests).toHaveLength(4));
    rerender({ visibleRange: [4, 5] });
    await waitFor(() => expect(result.current.data.pages.get(2)).toHaveLength(2));
    expect(requests[4]?.body.cursor).toMatch(/^next:4:/);
    expect(requests[4]?.body.cursor).not.toContain(oldCursor!.replace(/^next:\d+:/, ""));
    expect(requests[4]?.body.seek).toBeUndefined();
  });

  it.each([
    ["search text", { q: "moon" }],
    ["library", { library_id: 7 }],
    ["explicit default sort", { explicit_sort: true }],
  ] satisfies [string, Partial<CatalogSearchState>][])(
    "keeps old cursors out of a new %s scope",
    async (_name, patch) => {
      const { requests } = catalogTransport();
      const { wrapper } = makeClient();
      const state = createCatalogSearchState("query", { q: "star" });
      const { result, rerender } = renderHook(
        ({ current, visibleRange }) =>
          useCatalogWindow(current, { limit: 2, includeTotal: false, visibleRange }),
        {
          wrapper,
          initialProps: { current: state, visibleRange: [2, 3] as [number, number] },
        },
      );
      await waitFor(() => expect(result.current.data.pages.get(1)).toHaveLength(2));
      const oldRoot = requests[1]!.body.cursor!.replace(/^next:\d+:/, "");
      rerender({ current: { ...state, ...patch }, visibleRange: [4, 5] });
      await waitFor(() => expect(result.current.data.pages.get(2)).toHaveLength(2));
      expect(requests).toHaveLength(4);
      expect(requests[2]?.body.cursor).toBeUndefined();
      expect(requests[3]?.body).toMatchObject({ seek: 4 });
      expect(requests[3]?.body.cursor).toMatch(/^root:/);
      expect(requests[3]?.body.cursor).not.toBe(oldRoot);
      if ("explicit_sort" in patch && patch.explicit_sort) {
        expect(requests[3]?.body.sort).toBe("added_at");
      }
    },
  );

  it("cancels an adjacent request when its search scope changes", async () => {
    const { requests, fetchMock, reply } = catalogTransport();
    fetchMock.mockImplementation(async (_url, options) => {
      const body = JSON.parse(String(options?.body)) as CatalogQueryBody;
      const signal = options?.signal;
      requests.push({ body, signal });
      if (!body.cursor) return reply(body);
      return new Promise((_resolve, reject) => {
        signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")), {
          once: true,
        });
      });
    });
    const { wrapper } = makeClient();
    const { rerender, unmount } = renderHook(
      ({ q }) =>
        useCatalogWindow(createCatalogSearchState("query", { q }), {
          limit: 2,
          includeTotal: false,
          visibleRange: [2, 3],
        }),
      { wrapper, initialProps: { q: "star" } },
    );
    await waitFor(() => expect(requests).toHaveLength(2));
    expect(requests[1]?.body.cursor).toMatch(/^next:2:/);
    expect(requests[1]?.signal?.aborted).toBe(false);
    rerender({ q: "moon" });
    await waitFor(() => expect(requests).toHaveLength(4));
    expect(requests[1]?.signal?.aborted).toBe(true);
    expect(requests[3]?.signal?.aborted).toBe(false);
    unmount();
    expect(requests[3]?.signal?.aborted).toBe(true);
  });

  it("reuses facet documents across ignored overlays but separates library and media scopes", async () => {
    const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(getCatalogFiltersOk));
    vi.stubGlobal("fetch", fetchMock);
    const { wrapper } = makeClient();
    const state = createCatalogSearchState("query", { q: "star" });
    const { result, rerender } = renderHook(({ current }) => useCatalogFilters(current), {
      wrapper,
      initialProps: { current: state },
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    rerender({
      current: {
        ...state,
        q: "moon",
        title: "Movies",
        explicit_sort: true,
        query_definition: {
          ...state.query_definition,
          groups: [{ match: "all", rules: [{ field: "year", op: "gte", value: 2000 }] }],
          sort: { field: "title", order: "asc" },
        },
      },
    });
    expect(result.current.isSuccess).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    rerender({
      current: { ...state, query_definition: { ...state.query_definition, library_ids: [7] } },
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain("library_id=7");
    rerender({
      current: { ...state, query_definition: { ...state.query_definition, media_scope: "movie" } },
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(String(fetchMock.mock.calls[2]?.[0])).toContain("type=movie");
  });
});
