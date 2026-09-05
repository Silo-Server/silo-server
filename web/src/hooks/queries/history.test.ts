// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import listHistoryOk from "../../../../contracts/api/v2/fixtures/list_history_ok.json";
import removeHistoryValidationFailed from "../../../../contracts/api/v2/fixtures/remove_history_entries_validation_failed.json";

import { setProfileId } from "@/api/client";
import { V2ProblemError } from "@/api/v2/request";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { useHistory, useRemoveHistory } from "./history";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

vi.mock("./mediaSurfaceRefresh", () => ({
  invalidateMediaSurfaceQueries: vi.fn(async () => undefined),
}));

vi.mock("@/pages/homeSurfaceRefresh", () => ({
  bumpHomeRefreshSignal: vi.fn(),
}));

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  };
}

type FetchMock = ReturnType<typeof vi.fn<typeof fetch>>;

function stubFetch(handler: (url: URL, init?: RequestInit) => Response): FetchMock {
  const fetchMock = vi.fn<typeof fetch>(async (input, init) =>
    handler(new URL(String(input), "http://localhost"), init),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("history hooks on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("lists history as catalog cards from the listHistory fixture", async () => {
    const fetchMock = stubFetch((url) =>
      jsonResponse(
        url.searchParams.has("cursor") ? { items: [], page: { has_more: false } } : listHistoryOk,
      ),
    );

    const { result } = renderHook(() => useHistory(), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(String(url)).toBe("/api/v2/history");
    expect((init?.headers as Record<string, string>)["X-Profile-Id"]).toBe("p-owner");

    const cards = result.current.data?.pages.flatMap((page) => page.items) ?? [];
    expect(cards.map((c) => c.content_id)).toEqual(listHistoryOk.items.map((i) => i.content_id));
    expect(cards[0]?.type).toBe("series");
    expect(cards[0]?.title).toBe("Heat: The Series");
  });

  it("follows the cursor through an empty nonterminal page", async () => {
    const cursors: (string | null)[] = [];
    stubFetch((url) => {
      const cursor = url.searchParams.get("cursor");
      cursors.push(cursor);
      if (!cursor)
        return jsonResponse({
          ...listHistoryOk,
          items: [listHistoryOk.items[0]],
          page: { has_more: true, next_cursor: "older" },
        });
      if (cursor === "older")
        return jsonResponse({ items: [], page: { has_more: true, next_cursor: "oldest" } });
      return jsonResponse({
        items: [{ ...listHistoryOk.items[0], content_id: "movie:last" }],
        page: { has_more: false },
      });
    });
    const { result } = renderHook(() => useHistory(), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(cursors).toEqual([null]);
    expect(result.current.data?.pages).toHaveLength(1);
    await act(async () => {
      await result.current.fetchNextPage();
    });
    await waitFor(() => expect(result.current.data?.pages[1]?.items).toEqual([]));
    expect(result.current.hasNextPage).toBe(true);
    await act(() => result.current.fetchNextPage());
    await waitFor(() => expect(result.current.hasNextPage).toBe(false));
    expect(cursors).toEqual([null, "older", "oldest"]);
    expect(
      result.current.data?.pages.flatMap((page) => page.items.map((item) => item.content_id)),
    ).toEqual([listHistoryOk.items[0]?.content_id, "movie:last"]);
  });

  it("posts removal targets to removeHistoryEntries and treats 204 as success", async () => {
    const fetchMock = stubFetch(() => new Response(null, { status: 204 }));

    const { result } = renderHook(() => useRemoveHistory(), { wrapper: createWrapper() });
    await act(async () => {
      await result.current.mutateAsync([{ content_id: "series:heat", scope: "show" }]);
    });

    const [url, init] = fetchMock.mock.calls[0] ?? [];
    expect(String(url)).toBe("/api/v2/history/remove");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      targets: [{ content_id: "series:heat", scope: "show" }],
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
  });

  it("surfaces the validation problem for a rejected removal target", async () => {
    stubFetch(
      () =>
        new Response(JSON.stringify(removeHistoryValidationFailed), {
          status: 422,
          headers: { "Content-Type": "application/problem+json" },
        }),
    );

    const { result } = renderHook(() => useRemoveHistory(), { wrapper: createWrapper() });
    const failure = await act(() =>
      result.current.mutateAsync([{ content_id: "series:heat", scope: "show" }]).then(
        () => undefined,
        (err: unknown) => err,
      ),
    );

    expect(failure).toBeInstanceOf(V2ProblemError);
    const problem = (failure as V2ProblemError).problem;
    expect(problem.status).toBe(422);
    expect(problem.errors?.[0]?.location).toBe("body.targets[0].scope");
  });
});
