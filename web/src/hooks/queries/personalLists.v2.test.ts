// @vitest-environment jsdom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import listFavoritesOk from "../../../../contracts/api/v2/fixtures/list_favorites_ok.json";
import listWatchlistOk from "../../../../contracts/api/v2/fixtures/list_watchlist_ok.json";
import setRatingOutOfRange from "../../../../contracts/api/v2/fixtures/set_rating_out_of_range.json";
import addFavoriteNotFound from "../../../../contracts/api/v2/fixtures/add_favorite_not_found.json";

import { setProfileId } from "@/api/client";
import { V2ProblemError } from "@/api/v2/request";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { useFavorites, useToggleFavorite } from "./favorites";
import { useDeleteRating, useSetRating } from "./ratings";
import { useToggleWatchlist, useWatchlist } from "./watchlist";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

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

function callOf(fetchMock: FetchMock, index = 0) {
  const call = fetchMock.mock.calls[index];
  const init = call?.[1];
  return {
    url: String(call?.[0]),
    method: init?.method ?? "GET",
    headers: (init?.headers ?? {}) as Record<string, string>,
    body: init?.body,
  };
}

function noContent() {
  return new Response(null, { status: 204 });
}

describe("personal lists on the v2 contract", () => {
  beforeEach(() => {
    installPolicyStorageMocks();
    setProfileId("p-owner");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("lists favorites as browse cards from the items envelope", async () => {
    const fetchMock = stubFetch(() => jsonResponse(listFavoritesOk));

    const { result } = renderHook(() => useFavorites(), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    const call = callOf(fetchMock);
    expect(call.url).toBe("/api/v2/favorites");
    expect(call.headers["X-Profile-Id"]).toBe("p-owner");
    expect(result.current.data?.map((item) => [item.content_id, item.type, item.title])).toEqual(
      listFavoritesOk.items.map((item) => [item.content_id, item.type, item.title]),
    );
    expect(result.current.data?.[0]?.poster_url).toBe("");
  });

  it("lists the watchlist as browse cards from the items envelope", async () => {
    const fetchMock = stubFetch(() => jsonResponse(listWatchlistOk));

    const { result } = renderHook(() => useWatchlist(), { wrapper: createWrapper() });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));

    expect(callOf(fetchMock).url).toBe("/api/v2/watchlist");
    expect(result.current.data?.map((item) => item.content_id)).toEqual(
      listWatchlistOk.items.map((item) => item.content_id),
    );
  });

  it("toggles a favorite with PUT to add and DELETE to remove", async () => {
    const fetchMock = stubFetch(() => noContent());

    const { result } = renderHook(() => useToggleFavorite("movie:c"), {
      wrapper: createWrapper(),
    });
    await act(async () => {
      await result.current.mutateAsync(false);
      await result.current.mutateAsync(true);
    });

    expect(callOf(fetchMock, 0)).toMatchObject({
      url: "/api/v2/favorites/movie%3Ac",
      method: "PUT",
    });
    expect(callOf(fetchMock, 1)).toMatchObject({
      url: "/api/v2/favorites/movie%3Ac",
      method: "DELETE",
    });
  });

  it("toggles a watchlist entry with PUT to add and DELETE to remove", async () => {
    const fetchMock = stubFetch(() => noContent());

    const { result } = renderHook(() => useToggleWatchlist("movie:c"), {
      wrapper: createWrapper(),
    });
    await act(async () => {
      await result.current.mutateAsync(false);
      await result.current.mutateAsync(true);
    });

    expect(callOf(fetchMock, 0)).toMatchObject({
      url: "/api/v2/watchlist/movie%3Ac",
      method: "PUT",
    });
    expect(callOf(fetchMock, 1)).toMatchObject({
      url: "/api/v2/watchlist/movie%3Ac",
      method: "DELETE",
    });
  });

  it("surfaces the not_found problem when adding a favorite for an unknown item", async () => {
    stubFetch(() => jsonResponse(addFavoriteNotFound, addFavoriteNotFound.status));

    const { result } = renderHook(() => useToggleFavorite("movie:missing"), {
      wrapper: createWrapper(),
    });
    const error = await act(() => result.current.mutateAsync(false).catch((err) => err));

    expect(error).toBeInstanceOf(V2ProblemError);
    expect((error as V2ProblemError).status).toBe(404);
    expect((error as V2ProblemError).problemType).toBe("not_found");
  });

  it("sets and deletes a rating through the ratings operations", async () => {
    const fetchMock = stubFetch(() => noContent());

    const set = renderHook(() => useSetRating("movie:c"), { wrapper: createWrapper() });
    const remove = renderHook(() => useDeleteRating("movie:c"), { wrapper: createWrapper() });
    await act(async () => {
      await set.result.current.mutateAsync(4);
      await remove.result.current.mutateAsync();
    });

    const setCall = callOf(fetchMock, 0);
    expect(setCall).toMatchObject({ url: "/api/v2/ratings/movie%3Ac", method: "PUT" });
    expect(JSON.parse(String(setCall.body))).toEqual({ rating: 4 });
    expect(callOf(fetchMock, 1)).toMatchObject({
      url: "/api/v2/ratings/movie%3Ac",
      method: "DELETE",
    });
  });

  it("rejects an out-of-range rating with the validation problem", async () => {
    stubFetch(() => jsonResponse(setRatingOutOfRange, setRatingOutOfRange.status));

    const { result } = renderHook(() => useSetRating("movie:c"), { wrapper: createWrapper() });
    const error = await act(() => result.current.mutateAsync(9).catch((err) => err));

    expect(error).toBeInstanceOf(V2ProblemError);
    expect((error as V2ProblemError).status).toBe(422);
    expect((error as V2ProblemError).problem.errors?.[0]).toEqual(setRatingOutOfRange.errors[0]);
  });
});
