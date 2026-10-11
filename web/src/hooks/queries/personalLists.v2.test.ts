// @vitest-environment jsdom

import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement } from "react";
import type { ReactNode } from "react";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import listFavoritesOk from "../../../../contracts/api/v2/fixtures/list_favorites_ok.json";

import { setProfileId } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

import { useFavorites, useToggleFavorite } from "./favorites";
import { PERSONAL_STATE_WRITE_TIMEOUT_MS } from "./personalStateWrites";
import { catalogKeys, itemKeys } from "./keys";
import { useDeleteRating, useSetRating } from "./ratings";
import { useToggleWatchlist } from "./watchlist";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function createWrapper(
  client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  }),
) {
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
    vi.mocked(toast.error).mockClear();
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

  it("sets and deletes ratings, restoring both detail caches after rejected writes", async () => {
    const fetchMock = stubFetch(() => noContent());

    const client = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    const wrapper = createWrapper(client);
    const set = renderHook(() => useSetRating("movie:c"), { wrapper });
    const remove = renderHook(() => useDeleteRating("movie:c"), { wrapper });
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

    const detailKeys = [catalogKeys.itemDetail("movie:c"), itemKeys.detail("movie:c")];
    const otherKey = catalogKeys.itemDetail("movie:other");
    for (const key of detailKeys) {
      client.setQueryData(key, { content_id: "movie:c", user_rating: 3 });
    }
    let otherRating = 1;
    client.setQueryData(otherKey, { content_id: "movie:other", user_rating: otherRating });
    fetchMock.mockImplementation(async () => {
      // Another item's update can land while this mutation is in flight.
      // Its cache must not be captured and rolled back with this item's draft.
      client.setQueryData(otherKey, { content_id: "movie:other", user_rating: ++otherRating });
      return new Response(
        JSON.stringify({
          type: "https://siloserver.org/problems/validation_failed",
          title: "Validation failed",
          status: 422,
          detail: "Rating refused",
          instance: "/api/v2/ratings/movie%3Ac",
        }),
        { status: 422, headers: { "Content-Type": "application/problem+json" } },
      );
    });

    try {
      for (const mutation of [
        () => set.result.current.mutateAsync(2),
        () => remove.result.current.mutateAsync(),
      ]) {
        await act(async () => {
          await expect(mutation()).rejects.toThrow("Rating refused");
        });
        for (const key of detailKeys) {
          expect(client.getQueryData(key)).toMatchObject({ user_rating: 3 });
        }
        expect(client.getQueryData(otherKey)).toMatchObject({ user_rating: otherRating });
      }
    } finally {
      set.unmount();
      remove.unmount();
      client.clear();
    }
  });

  describe("when the server cannot be reached", () => {
    afterEach(() => {
      onlineManager.setOnline(true);
      vi.useRealTimers();
    });

    const unreachable = () =>
      stubFetch(() => {
        throw new TypeError("Failed to fetch");
      });

    it.each([
      ["favorite", () => useToggleFavorite("movie:c"), "Failed to update favorites"],
      ["watchlist entry", () => useToggleWatchlist("movie:c"), "Failed to update watchlist"],
    ])("sends a %s toggle while offline and reports the failure", async (_kind, hook, message) => {
      onlineManager.setOnline(false);
      const fetchMock = unreachable();

      const { result } = renderHook(hook, { wrapper: createWrapper() });
      await act(async () => {
        await expect(result.current.mutateAsync(false)).rejects.toThrow();
      });

      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(result.current.isPaused).toBe(false);
      expect(toast.error).toHaveBeenCalledWith(message);
    });

    it("rolls back a rating set or cleared while offline and reports the failure", async () => {
      onlineManager.setOnline(false);
      unreachable();
      const client = new QueryClient({
        defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
      });
      client.setQueryData(catalogKeys.itemDetail("movie:c"), {
        content_id: "movie:c",
        user_rating: 3,
      });
      const wrapper = createWrapper(client);
      const set = renderHook(() => useSetRating("movie:c"), { wrapper });
      const remove = renderHook(() => useDeleteRating("movie:c"), { wrapper });

      for (const mutation of [
        () => set.result.current.mutateAsync(5),
        () => remove.result.current.mutateAsync(),
      ]) {
        await act(async () => {
          await expect(mutation()).rejects.toThrow();
        });
        expect(client.getQueryData(catalogKeys.itemDetail("movie:c"))).toMatchObject({
          user_rating: 3,
        });
      }
      expect(toast.error).toHaveBeenCalledTimes(2);
      expect(toast.error).toHaveBeenCalledWith("Failed to update rating");
      client.clear();
    });

    it("gives up on a toggle the server never answers", async () => {
      vi.useFakeTimers();
      // A server that accepts the connection and then stops answering.
      vi.stubGlobal(
        "fetch",
        vi.fn<typeof fetch>(
          (_input, init) =>
            new Promise((_resolve, reject) => {
              init?.signal?.addEventListener("abort", () => reject(init.signal?.reason));
            }),
        ),
      );

      const { result } = renderHook(() => useToggleFavorite("movie:c"), {
        wrapper: createWrapper(),
      });
      let outcome: Promise<unknown> = Promise.resolve();
      act(() => {
        outcome = result.current.mutateAsync(false);
      });
      const rejected = expect(outcome).rejects.toThrow("did not respond");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(PERSONAL_STATE_WRITE_TIMEOUT_MS);
      });
      await rejected;
      expect(toast.error).toHaveBeenCalledWith("Failed to update favorites");
    });
  });
});
