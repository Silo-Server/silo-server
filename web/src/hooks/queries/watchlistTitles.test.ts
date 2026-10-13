import { QueryClient } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WatchlistTitle } from "@/api/v2/watchlistTitles";
import { scheduleMediaSurfaceInvalidation } from "./mediaSurfaceRefresh";
import {
  patchCachedInWatchlist,
  refreshAfterTitlesRead,
  watchlistAddToast,
  watchlistTitlesRefetchInterval,
} from "./watchlistTitles";

vi.mock("./mediaSurfaceRefresh", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./mediaSurfaceRefresh")>()),
  scheduleMediaSurfaceInvalidation: vi.fn(),
}));

describe("watchlistAddToast", () => {
  const input = { title: "Heat", request: { requestable: true } };

  it("says a title the library has was only added", () => {
    expect(
      watchlistAddToast({ item_id: "abc", request: { requestable: false } }, input, true),
    ).toEqual({ title: "Added to your watchlist" });
  });

  it("says the add also requested the title", () => {
    expect(
      watchlistAddToast(
        { request: { requestable: false, status: "pending", requested_by_viewer: true } },
        input,
        true,
      ),
    ).toEqual({
      title: "Added to your watchlist and requested",
      description:
        "We'll let you know when Heat is available. It moves into your watchlist on its own.",
    });
  });

  it("says the add followed someone else's request", () => {
    expect(
      watchlistAddToast(
        { request: { requestable: false, status: "pending", following: true } },
        input,
        true,
      ),
    ).toEqual({
      title: "Added to your watchlist",
      description: "We'll let you know when Heat is available.",
    });
  });

  it("stays quiet about a request the viewer already had", () => {
    const requested = { requestable: false, status: "pending" as const, requested_by_viewer: true };
    expect(
      watchlistAddToast({ request: requested }, { title: "Heat", request: requested }, true),
    ).toEqual({ title: "Added to your watchlist" });
  });

  it("gives the reason a request was refused", () => {
    expect(
      watchlistAddToast({ request: { requestable: false, reason: "quota_exceeded" } }, input, true),
    ).toEqual({
      title: "Added to your watchlist",
      description: "It wasn't requested: request limit reached.",
    });
  });

  it("says watchlist requests are off", () => {
    expect(watchlistAddToast({ request: { requestable: true } }, input, false)).toEqual({
      title: "Added to your watchlist",
      description: "It wasn't requested, because watchlist requests are off.",
    });
    expect(watchlistAddToast({ request: { requestable: true } }, input, undefined)).toEqual({
      title: "Added to your watchlist",
      description: "It wasn't requested.",
    });
  });
});

describe("patchCachedInWatchlist", () => {
  const heat = { media_type: "movie", tmdb_id: 949, title: "Heat", request: { requestable: true } };
  const other = {
    media_type: "series",
    tmdb_id: 949,
    title: "Other",
    request: { requestable: true },
  };

  it("sets in_watchlist on the matching title at any depth", () => {
    const data = { sections: [{ items: [heat, other] }], detail: { ...heat, in_watchlist: false } };
    const patched = patchCachedInWatchlist(data, "movie", 949, true) as typeof data;
    expect(patched.sections[0]!.items[0]).toEqual({ ...heat, in_watchlist: true });
    expect(patched.detail.in_watchlist).toBe(true);
    // A series with the same TMDB number is a different title.
    expect(patched.sections[0]!.items[1]).toBe(other);
  });

  it("keeps the references of anything it does not change", () => {
    const data = { pages: [{ items: [other] }] };
    expect(patchCachedInWatchlist(data, "movie", 949, true)).toBe(data);
    const already = { items: [{ ...heat, in_watchlist: true }] };
    expect(patchCachedInWatchlist(already, "movie", 949, true)).toBe(already);
  });

  it("ignores objects without a request state", () => {
    const person = { media_type: "movie", tmdb_id: 949, name: "Not a title" };
    expect(patchCachedInWatchlist(person, "movie", 949, true)).toBe(person);
  });
});

describe("refreshAfterTitlesRead", () => {
  const title = (tmdbID: number) => ({ media_type: "movie", tmdb_id: tmdbID }) as WatchlistTitle;
  let queryClient: QueryClient;
  let invalidated: unknown[][];

  beforeEach(() => {
    vi.mocked(scheduleMediaSurfaceInvalidation).mockClear();
    queryClient = new QueryClient();
    invalidated = [];
    const invalidate = queryClient.invalidateQueries.bind(queryClient);
    vi.spyOn(queryClient, "invalidateQueries").mockImplementation((filters, options) => {
      for (const query of queryClient.getQueryCache().findAll(filters)) {
        invalidated.push([...query.queryKey]);
      }
      return invalidate(filters, options);
    });
    queryClient.setQueryData(["catalog", "list", { source: "watchlist" }], { items: [] });
    queryClient.setQueryData(["catalog", "list", { source: "favorites" }], { items: [] });
    queryClient.setQueryData(["watchlist", "titles"], []);
  });

  it("refreshes the library watchlist grid after every read, not other lists or itself", () => {
    refreshAfterTitlesRead(queryClient, undefined, [title(1)]);
    expect(invalidated).toEqual([["catalog", "list", { source: "watchlist" }]]);
    expect(scheduleMediaSurfaceInvalidation).not.toHaveBeenCalled();
  });

  it("refreshes the library surfaces when a title the tab showed is gone", () => {
    refreshAfterTitlesRead(queryClient, [title(1), title(2)], [title(2)]);
    expect(scheduleMediaSurfaceInvalidation).toHaveBeenCalledTimes(1);
  });

  it("settles when the same titles come back", () => {
    refreshAfterTitlesRead(queryClient, [title(2)], [title(2), title(3)]);
    expect(scheduleMediaSurfaceInvalidation).not.toHaveBeenCalled();
  });
});

describe("watchlistTitlesRefetchInterval", () => {
  const title = (download?: object) =>
    ({
      media_type: "movie",
      tmdb_id: 1,
      request: { requestable: false, download },
    }) as WatchlistTitle;

  it("polls while a title downloads", () => {
    expect(watchlistTitlesRefetchInterval([title(), title({ phase: "downloading" })])).toBe(30_000);
  });

  it("stays quiet otherwise", () => {
    expect(watchlistTitlesRefetchInterval([title()])).toBe(false);
    expect(watchlistTitlesRefetchInterval(undefined)).toBe(false);
  });
});
