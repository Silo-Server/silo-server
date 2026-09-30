import { describe, expect, it } from "vitest";
import { patchCachedInWatchlist, watchlistAddToast } from "./watchlistTitles";

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
