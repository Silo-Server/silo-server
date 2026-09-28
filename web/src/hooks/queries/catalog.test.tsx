import { renderToStaticMarkup } from "react-dom/server";
import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  v2: vi.fn(),
  useQueries: vi.fn(),
  useQuery: vi.fn(),
}));

vi.mock("@tanstack/react-query", () => ({
  keepPreviousData: Symbol("keepPreviousData"),
  useQueries: (...args: unknown[]) => mocks.useQueries(...args),
  useQuery: (...args: unknown[]) => mocks.useQuery(...args),
}));

vi.mock("@/api/v2/request", () => ({
  v2: (...args: unknown[]) => mocks.v2(...args),
}));

import type { CatalogResponse } from "@/api/types";
import { createCatalogSearchState, useCatalogWindow } from "./catalog";

function makePage(offset: number, limit = 60): CatalogResponse {
  return {
    total: 1000,
    has_more: true,
    title: "Catalog",
    items: Array.from({ length: limit }, (_, index) => ({
      content_id: `item-${offset + index}`,
      type: "movie" as const,
      title: `Item ${offset + index}`,
      year: 2024,
      genres: [],
      content_rating: "PG",
      status: "matched" as const,
      rating_imdb: null,
      overview: "",
      poster_url: "",
      poster_thumbhash: "",
      backdrop_url: "",
      backdrop_thumbhash: "",
    })),
  };
}

describe("useCatalogWindow", () => {
  beforeEach(() => {
    mocks.v2.mockReset();
    mocks.useQueries.mockReset();
    mocks.useQuery.mockReset();
  });

  it("requests only the visible distant window and one buffer on each side", () => {
    const state = createCatalogSearchState("favorites");
    mocks.useQuery.mockReturnValue({
      data: { ...makePage(0), total: 100000, snapshot: "opaque-window" },
      isLoading: false,
    });
    mocks.useQueries.mockImplementation(({ queries }) => queries.map(() => ({ isLoading: true })));

    renderHook(() => useCatalogWindow(state, { visibleRange: [60000, 60059] }));

    const queries = mocks.useQueries.mock.calls[0]?.[0].queries;
    expect(
      queries.map(
        (query: { queryKey: [string, string, { offset: number }] }) => query.queryKey[2].offset,
      ),
    ).toEqual([59940, 60000, 60060]);
    expect(
      queries.every(
        (query: { queryKey: [string, string, { snapshot: string }] }) =>
          query.queryKey[2].snapshot === "opaque-window",
      ),
    ).toBe(true);
  });

  it("does not overscan beyond the exact result count", () => {
    mocks.useQuery.mockReturnValue({
      data: { ...makePage(0), total: 125, total_exact: true, snapshot: "opaque-window" },
      isLoading: false,
    });
    mocks.useQueries.mockImplementation(({ queries }) => queries.map(() => ({ isLoading: true })));
    renderHook(() =>
      useCatalogWindow(createCatalogSearchState("favorites"), { visibleRange: [120, 124] }),
    );
    const queries = mocks.useQueries.mock.calls[0]?.[0].queries;
    expect(
      queries.map(
        (query: { queryKey: [string, string, { offset: number }] }) => query.queryKey[2].offset,
      ),
    ).toEqual([60, 120]);
  });

  it("separates filter edits and server-resolved sort from cached explicit-sort windows", () => {
    const state = createCatalogSearchState("favorites");
    mocks.useQuery.mockReturnValue({ data: undefined, isLoading: true });
    mocks.useQueries.mockReturnValue([]);
    const { rerender } = renderHook(({ current }) => useCatalogWindow(current), {
      initialProps: { current: state },
    });
    const initialKey = mocks.useQuery.mock.calls.at(-1)?.[0].queryKey;
    rerender({ current: { ...state, sort_from_server: true } });
    const serverSortKey = mocks.useQuery.mock.calls.at(-1)?.[0].queryKey;
    expect(serverSortKey).not.toEqual(initialKey);
    rerender({
      current: {
        ...state,
        query_definition: {
          ...state.query_definition,
          groups: [{ match: "all", rules: [{ field: "year", op: "gte", value: 2000 }] }],
        },
      },
    });
    expect(mocks.useQuery.mock.calls.at(-1)?.[0].queryKey).not.toEqual(initialKey);
  });

  it("does not assign stale placeholder data to newly visible page indices", () => {
    const state = createCatalogSearchState("favorites");
    const limit = 60;

    function Harness({ visibleRange }: { visibleRange: [number, number] }) {
      const result = useCatalogWindow(state, { limit, visibleRange });
      return (
        <div
          data-page6={result.data.pages.get(6)?.[0]?.content_id ?? "missing"}
          data-page7={result.data.pages.get(7)?.[0]?.content_id ?? "missing"}
        />
      );
    }

    // Page 0 is now fetched via useQuery (separate from the windowed pages).
    const page0Data = { ...makePage(0, limit), snapshot: "2026-01-01T00:00:00Z" };
    mocks.useQuery.mockReturnValue({ data: page0Data, isLoading: false });

    mocks.useQueries.mockImplementation(
      ({
        queries,
      }: {
        queries: Array<{
          queryKey: [string, string, { offset?: number }];
          placeholderData?: unknown;
        }>;
      }) => {
        const offsets = queries.map((query) => query.queryKey[2].offset ?? 0);
        const hasPlaceholderData = queries.some((query) => "placeholderData" in query);

        if (offsets.join(",") === "60") {
          return [{ data: makePage(60, limit), isLoading: false }];
        }

        if (offsets.join(",") === "360,420,480") {
          return [
            hasPlaceholderData
              ? { data: makePage(60, limit), isLoading: true, isPlaceholderData: true }
              : { data: undefined, isLoading: true },
            hasPlaceholderData
              ? { data: makePage(120, limit), isLoading: true, isPlaceholderData: true }
              : { data: undefined, isLoading: true },
            { data: undefined, isLoading: true },
          ];
        }

        throw new Error(`Unexpected query offsets: ${offsets.join(",")}`);
      },
    );

    renderToStaticMarkup(<Harness visibleRange={[0, limit - 1]} />);
    const markup = renderToStaticMarkup(<Harness visibleRange={[420, 479]} />);

    expect(markup).toContain('data-page6="missing"');
    expect(markup).toContain('data-page7="missing"');
  });

  it("keeps the previous search grid mounted while page 0 is replacing", () => {
    const state = createCatalogSearchState("query", { q: "heater" });
    const limit = 60;
    let page0Query:
      | {
          placeholderData?: (previous: CatalogResponse) => CatalogResponse;
          gcTime?: number;
          retry?: boolean;
        }
      | undefined;
    let pageQueries: Array<{ enabled?: boolean; gcTime?: number; retry?: boolean }> | undefined;

    mocks.useQuery.mockImplementation((query) => {
      page0Query = query;
      return {
        data: makePage(0, limit),
        isLoading: true,
        isPlaceholderData: true,
      };
    });
    mocks.useQueries.mockImplementation(({ queries }) => {
      pageQueries = queries;
      return queries.map(() => ({ data: undefined, isLoading: false }));
    });

    function Harness() {
      useCatalogWindow(state, { limit, visibleRange: [60, 119] });
      return null;
    }

    renderToStaticMarkup(<Harness />);

    const previous = makePage(0, limit);
    expect(page0Query?.placeholderData?.(previous)).toBe(previous);
    expect(page0Query).toMatchObject({ gcTime: 30_000, retry: false });
    expect(pageQueries?.every((query) => query.enabled === false)).toBe(true);
    expect(pageQueries?.every((query) => query.gcTime === 30_000 && query.retry === false)).toBe(
      true,
    );
  });

  it("surfaces and retries a failed visible follow-on page", async () => {
    const state = createCatalogSearchState("query", { q: "star" });
    const limit = 60;
    const pageError = new Error("search_timeout");
    const page0Refetch = vi.fn().mockResolvedValue(undefined);
    const failedPageRefetch = vi.fn().mockResolvedValue(undefined);

    mocks.useQuery.mockReturnValue({
      data: { ...makePage(0, limit), snapshot: "2026-01-01T00:00:00Z" },
      isLoading: false,
      isError: false,
      isPlaceholderData: false,
      error: null,
      refetch: page0Refetch,
    });
    mocks.useQueries.mockReturnValue([
      {
        data: undefined,
        isLoading: false,
        isError: true,
        error: pageError,
        refetch: failedPageRefetch,
      },
    ]);

    const { result } = renderHook(() =>
      useCatalogWindow(state, { limit, visibleRange: [60, 119] }),
    );
    expect(result.current.isError).toBe(true);
    expect(result.current.error).toBe(pageError);

    await result.current.refetch();
    expect(page0Refetch).toHaveBeenCalledOnce();
    expect(failedPageRefetch).toHaveBeenCalledOnce();
  });

  it("keeps a successful visible page when only its off-screen buffer fails", async () => {
    const state = createCatalogSearchState("favorites");
    const limit = 60;
    const page0Refetch = vi.fn().mockResolvedValue(undefined);
    const failedBufferRefetch = vi.fn().mockResolvedValue(undefined);

    mocks.useQuery.mockReturnValue({
      data: { ...makePage(0, limit), snapshot: "2026-01-01T00:00:00Z" },
      isLoading: false,
      isError: false,
      isPlaceholderData: false,
      error: null,
      refetch: page0Refetch,
    });
    mocks.useQueries.mockReturnValue([
      {
        data: undefined,
        isLoading: false,
        isError: true,
        error: new Error("buffer failed"),
        refetch: failedBufferRefetch,
      },
    ]);

    const { result } = renderHook(() =>
      useCatalogWindow(state, { limit, visibleRange: [0, limit - 1] }),
    );
    expect(result.current.data.pages.get(0)).toHaveLength(limit);
    expect(result.current.isError).toBe(false);
    expect(result.current.error).toBeUndefined();

    await result.current.refetch();
    expect(page0Refetch).toHaveBeenCalledOnce();
    expect(failedBufferRefetch).not.toHaveBeenCalled();
  });

  it("estimates window size from has_more when total is omitted", () => {
    const state = createCatalogSearchState("query", { library_id: 7 });
    const limit = 60;

    function Harness() {
      const result = useCatalogWindow(state, { limit, includeTotal: false });
      return <div data-total={result.data.totalItems} />;
    }

    mocks.useQuery.mockReturnValue({
      data: {
        ...makePage(0, limit),
        total: 0,
        total_exact: false,
        snapshot: "2026-01-01T00:00:00Z",
      },
      isLoading: false,
    });
    mocks.useQueries.mockReturnValue([]);

    const markup = renderToStaticMarkup(<Harness />);

    expect(markup).toContain('data-total="360"');
  });

  it("does not count empty buffered pages as loaded items when total is omitted", () => {
    const state = createCatalogSearchState("query", { library_id: 7 });
    const limit = 60;

    function Harness() {
      const result = useCatalogWindow(state, { limit, includeTotal: false });
      return <div data-total={result.data.totalItems} />;
    }

    mocks.useQuery.mockReturnValue({
      data: {
        ...makePage(0, 3),
        total: 0,
        total_exact: false,
        has_more: false,
        snapshot: "2026-01-01T00:00:00Z",
      },
      isLoading: false,
    });
    mocks.useQueries.mockReturnValue([
      {
        data: {
          ...makePage(60, 0),
          total: 0,
          total_exact: false,
          has_more: false,
          items: [],
        },
        isLoading: false,
      },
    ]);

    const markup = renderToStaticMarkup(<Harness />);

    expect(markup).toContain('data-total="3"');
  });

  it("requests follow-on pages for non-snapshot sources after page 0 loads", () => {
    const state = createCatalogSearchState("history");
    const limit = 60;

    function Harness() {
      useCatalogWindow(state, { limit, visibleRange: [120, 179] });
      return null;
    }

    let pageQueries:
      | Array<{
          queryKey: [string, string, { offset?: number; snapshot?: string }];
          enabled?: boolean;
        }>
      | undefined;

    mocks.useQuery.mockReturnValue({
      data: {
        ...makePage(0, limit),
        total_exact: true,
      },
      isLoading: false,
    });
    mocks.useQueries.mockImplementation(({ queries }) => {
      pageQueries = queries;
      return [{ data: makePage(120, limit), isLoading: false }];
    });

    renderToStaticMarkup(<Harness />);

    expect(pageQueries).toHaveLength(3);
    expect(pageQueries?.map((query) => query.queryKey[2]?.offset)).toEqual([60, 120, 180]);
    expect(pageQueries?.[1]?.queryKey[2]).toMatchObject({
      offset: 120,
    });
    expect(pageQueries?.every((query) => query.queryKey[2]?.snapshot === undefined)).toBe(true);
    expect(pageQueries?.every((query) => query.enabled === true)).toBe(true);
  });

  it("only requests exact totals on page 0 when includeTotal is enabled", async () => {
    const state = createCatalogSearchState("query", {
      library_id: 7,
      q: "heat",
    });
    const limit = 60;

    function Harness() {
      useCatalogWindow(state, { limit, includeTotal: true, visibleRange: [120, 179] });
      return null;
    }

    const page0Data = {
      ...makePage(0, limit),
      total_exact: true,
      snapshot: "2026-01-01T00:00:00Z",
    };
    const page2Data = {
      ...makePage(120, limit),
      total: 0,
      total_exact: false,
      snapshot: "2026-01-01T00:00:00Z",
    };
    let page0Query:
      | {
          queryFn: (context: { signal: AbortSignal }) => Promise<CatalogResponse>;
          queryKey: [string, string, { include_total?: boolean; offset?: number }];
        }
      | undefined;
    let pageQueries:
      | Array<{
          queryFn: (context: { signal: AbortSignal }) => Promise<CatalogResponse>;
          queryKey: [
            string,
            string,
            { include_total?: boolean; offset?: number; snapshot?: string },
          ];
        }>
      | undefined;

    mocks.useQuery.mockImplementation((query) => {
      page0Query = query;
      return { data: page0Data, isLoading: false };
    });
    mocks.useQueries.mockImplementation(({ queries }) => {
      pageQueries = queries;
      return [{ data: page2Data, isLoading: false }];
    });
    mocks.v2
      .mockResolvedValueOnce({ ...page0Data, window_cursor: page0Data.snapshot })
      .mockResolvedValueOnce({ ...page2Data, window_cursor: page2Data.snapshot });

    renderToStaticMarkup(<Harness />);

    expect(page0Query?.queryKey[2]).toMatchObject({
      include_total: true,
      offset: 0,
    });
    expect(pageQueries).toHaveLength(1);
    expect(pageQueries?.[0]?.queryKey[2]).toMatchObject({
      include_total: false,
      offset: 120,
      snapshot: "2026-01-01T00:00:00Z",
    });

    const signal = new AbortController().signal;
    await page0Query?.queryFn({ signal });
    await pageQueries?.[0]?.queryFn({ signal });

    expect(mocks.v2.mock.calls[0]?.[0]).toBe("POST /api/v2/catalog/query");
    expect(mocks.v2.mock.calls[0]?.[1]).toMatchObject({
      body: { limit, skip_total: undefined, seek: undefined },
      signal,
    });
    expect(mocks.v2.mock.calls[1]?.[1]).toMatchObject({
      body: { limit, skip_total: true, seek: 120, cursor: page0Data.snapshot },
      signal,
    });
  });
});
