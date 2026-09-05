// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import createTasteSeedOk from "../../../../contracts/api/v2/fixtures/create_taste_seed_ok.json";
import getDiscoverOk from "../../../../contracts/api/v2/fixtures/get_discover_ok.json";
import getRecommendationSectionOk from "../../../../contracts/api/v2/fixtures/get_recommendation_section_ok.json";
import getTasteProfileOk from "../../../../contracts/api/v2/fixtures/get_taste_profile_ok.json";
import getWatchTonightOk from "../../../../contracts/api/v2/fixtures/get_watch_tonight_ok.json";
import listSimilarOk from "../../../../contracts/api/v2/fixtures/list_similar_ok.json";
import listTasteSeedItemsOk from "../../../../contracts/api/v2/fixtures/list_taste_seed_items_ok.json";
import listWatchTonightCardsOk from "../../../../contracts/api/v2/fixtures/list_watch_tonight_cards_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import {
  useDiscover,
  useRecommendationSection,
  useSimilarItems,
  useSwipeCards,
  useTasteProfile,
  useWatchTonight,
} from "./recommendations";
import { useSubmitTasteSeed, useTasteSeedItems } from "./tasteSeed";

function createWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
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

function requestedUrl(fetchMock: FetchMock, index = 0): URL {
  return new URL(String(fetchMock.mock.calls[index]?.[0]), "http://localhost");
}

function headersOf(fetchMock: FetchMock, index = 0): Record<string, string> {
  return (fetchMock.mock.calls[index]?.[1]?.headers ?? {}) as Record<string, string>;
}

describe("recommendation reads on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("maps Discover rows to the screen's row shape", async () => {
    const fetchMock = stubFetch(() => jsonResponse(getDiscoverOk));

    const { result } = renderHook(() => useDiscover(), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(requestedUrl(fetchMock).pathname).toBe("/api/v2/recommendations/discover");
    expect(headersOf(fetchMock)["X-Profile-Id"]).toBe("p-owner");

    const rows = result.current.data?.rows ?? [];
    expect(rows.map((r) => [r.type, r.label, r.section_kind, r.section_key])).toEqual(
      getDiscoverOk.items.map((r) => [r.type, r.title, r.kind, r.key]),
    );
    expect(rows[0]?.items.map((i) => i.content_id)).toEqual(
      getDiscoverOk.items[0]?.items.map((i) => i.content_id),
    );
  });

  it("reads a keyed section through the kind path and key query", async () => {
    const fetchMock = stubFetch(() => jsonResponse(getRecommendationSectionOk));

    const { result } = renderHook(() => useRecommendationSection("genre", "Crime"), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    const url = requestedUrl(fetchMock);
    expect(url.pathname).toBe("/api/v2/recommendations/section/genre");
    expect(url.searchParams.get("key")).toBe("Crime");
    expect(result.current.data?.label).toBe(getRecommendationSectionOk.title);
    expect(result.current.data?.kind).toBe(getRecommendationSectionOk.kind);
    expect(result.current.data?.items.map((i) => i.content_id)).toEqual(
      getRecommendationSectionOk.items.map((i) => i.content_id),
    );
  });

  it("lists similar items as catalog cards with the limit declared", async () => {
    const fetchMock = stubFetch(() => jsonResponse(listSimilarOk));

    const { result } = renderHook(() => useSimilarItems("movie:heat-1995"), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    const url = requestedUrl(fetchMock);
    expect(url.pathname).toBe("/api/v2/recommendations/similar/movie%3Aheat-1995");
    expect(url.searchParams.get("limit")).toBe("12");
    expect(result.current.data?.items.map((i) => i.content_id)).toEqual(
      listSimilarOk.items.map((i) => i.content_id),
    );
  });

  it("returns the taste profile body as the contract declares it", async () => {
    stubFetch(() => jsonResponse(getTasteProfileOk));

    const { result } = renderHook(() => useTasteProfile(), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data?.top_genres).toEqual(getTasteProfileOk.top_genres);
    expect(result.current.data?.signal_counts).toEqual(getTasteProfileOk.signal_counts);
    expect(result.current.data?.updated_at).toBe(getTasteProfileOk.updated_at);
  });

  it("keeps the Watch Tonight source on each mapped card", async () => {
    stubFetch(() => jsonResponse(getWatchTonightOk));

    const { result } = renderHook(() => useWatchTonight(true), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(result.current.data?.is_cold).toBe(getWatchTonightOk.is_cold);
    expect(result.current.data?.items.map((i) => [i.content_id, i.watch_tonight_source])).toEqual(
      getWatchTonightOk.items.map((i) => [i.content_id, i.watch_tonight_source]),
    );
  });

  it("pages swipe cards by excluded ids sent as repeated query keys", async () => {
    const fetchMock = stubFetch(() => jsonResponse(listWatchTonightCardsOk));

    const { result } = renderHook(() => useSwipeCards(true, "discover", ["Crime", "Action"]), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    const url = requestedUrl(fetchMock);
    expect(url.pathname).toBe("/api/v2/recommendations/watch-tonight/cards");
    expect(url.searchParams.get("mode")).toBe("discover");
    expect(url.searchParams.getAll("genres")).toEqual(["Action", "Crime"]);

    const page = result.current.data?.pages[0];
    expect(page?.has_more).toBe(listWatchTonightCardsOk.has_more);
    expect(page?.cards.map((c) => c.content_id)).toEqual(
      listWatchTonightCardsOk.items.map((c) => c.content_id),
    );
    expect(page?.cards[0]?.cast).toEqual(listWatchTonightCardsOk.items[0]?.cast);
    expect(result.current.hasNextPage).toBe(listWatchTonightCardsOk.has_more);

    await result.current.fetchNextPage();
    await waitFor(() => expect(fetchMock.mock.calls.length).toBe(2));
    expect(requestedUrl(fetchMock, 1).searchParams.getAll("exclude_ids")).toEqual(
      listWatchTonightCardsOk.items.map((c) => c.content_id),
    );
  });
  it("stops a limited swipe session before exclusion requests exceed the schema", async () => {
    const fetchMock = stubFetch((url) => {
      const prior = url.searchParams.getAll("exclude_ids");
      expect(prior.length).toBeLessThanOrEqual(200);
      const count = Math.min(12, 200 - prior.length);
      return jsonResponse({
        ...listWatchTonightCardsOk,
        items: Array.from({ length: count }, (_, i) => ({
          ...listWatchTonightCardsOk.items[0],
          content_id: `movie:${prior.length + i}`,
        })),
        has_more: true,
        paging_limited: prior.length + count === 200,
      });
    });
    const { result } = renderHook(() => useSwipeCards(true, "discover", []), {
      wrapper: createWrapper(),
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data?.pages[0]?.cards).toHaveLength(12);
    expect(result.current.hasNextPage).toBe(true);
    for (let page = 1; page < 17; page++) {
      await act(async () => {
        const fetched = await result.current.fetchNextPage();
        expect(fetched.error).toBeNull();
      });
      await waitFor(() => expect(result.current.data?.pages).toHaveLength(page + 1));
    }
    expect(result.current.data?.pages.flatMap((page) => page.cards)).toHaveLength(200);
    expect(result.current.data?.pages.at(-1)?.has_more).toBe(true);
    expect(result.current.hasNextPage).toBe(false);
    await result.current.fetchNextPage();
    expect(fetchMock).toHaveBeenCalledTimes(17);
  });
});

describe("taste seeding on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("pages taste-seed items by cursor", async () => {
    const fetchMock = stubFetch(() => jsonResponse(listTasteSeedItemsOk));

    const { result } = renderHook(() => useTasteSeedItems(), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    const url = requestedUrl(fetchMock);
    expect(url.pathname).toBe("/api/v2/recommendations/taste-seed/items");
    expect(url.searchParams.get("limit")).toBe("30");
    expect(url.searchParams.has("cursor")).toBe(false);

    const page = result.current.data?.pages[0];
    expect(page?.items.map((i) => i.content_id)).toEqual(
      listTasteSeedItemsOk.items.map((i) => i.content_id),
    );
    expect(page?.next_cursor).toBe(listTasteSeedItemsOk.page.next_cursor);
  });

  it("submits the seed set as a JSON body", async () => {
    const fetchMock = stubFetch(() => jsonResponse(createTasteSeedOk));

    const { result } = renderHook(() => useSubmitTasteSeed(), { wrapper: createWrapper() });
    await result.current.mutateAsync(["movie:heat-1995"]);

    expect(requestedUrl(fetchMock).pathname).toBe("/api/v2/recommendations/taste-seed");
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("POST");
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      item_ids: ["movie:heat-1995"],
    });
  });
});
