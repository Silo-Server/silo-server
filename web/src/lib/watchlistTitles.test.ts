import { describe, expect, it } from "vitest";
import type { WatchlistTitle } from "@/api/v2/watchlistTitles";
import {
  WATCHLIST_NOT_IN_LIBRARY_TAB,
  WATCHLIST_TITLE_RANK,
  parseWatchlistTab,
  showWatchlistAutoRequestControl,
  sortWatchlistTitlesSoonestFirst,
  watchlistTitleNeedsAttention,
  watchlistTitlesAvailable,
  watchlistTitlesHint,
  watchlistTitleStatus,
} from "./watchlistTitles";

const NOW = new Date(2026, 8, 30, 12);

function title(overrides: Partial<WatchlistTitle> = {}): WatchlistTitle {
  return {
    media_type: "movie",
    tmdb_id: 1,
    title: "Heat",
    added_at: "2026-09-01T00:00:00Z",
    status: "active",
    request: { requestable: true },
    ...overrides,
  };
}

describe("watchlistTitleStatus", () => {
  it("shows download progress on the badge and the full status in the caption", () => {
    const status = watchlistTitleStatus(
      title({
        request: {
          requestable: false,
          status: "downloading",
          download: {
            phase: "downloading",
            percent: 43,
            downloads: 1,
            updated_at: NOW.toISOString(),
          },
        },
      }),
      NOW,
    );
    expect(status.badge).toBe("Downloading 43%");
    expect(status.badgeIcon).toBe("download");
    expect(status.caption).toMatch(/^Downloading · 43%/);
    expect(status.downloadPercent).toBe(43);
    expect(status.rank).toBe(WATCHLIST_TITLE_RANK.downloading);
    expect(status.attention).toBe(false);
  });

  it("leads with the release date while a request awaits approval", () => {
    const status = watchlistTitleStatus(
      title({ release_date: "2026-12-18", request: { requestable: false, status: "pending" } }),
      NOW,
    );
    expect(status.badge).toMatch(/^Out /);
    expect(status.badgeIcon).toBe("calendar");
    expect(status.caption).toBe(`${status.badge} · awaiting approval`);
    expect(status.rank).toBe(WATCHLIST_TITLE_RANK.dated);
  });

  it("reads an undated pending request as awaiting approval", () => {
    const status = watchlistTitleStatus(
      title({ release_date: "2001-01-01", request: { requestable: false, status: "pending" } }),
      NOW,
    );
    expect(status.badge).toBe("Awaiting approval");
    expect(status.caption).toBe("Awaiting approval");
    expect(status.rank).toBe(WATCHLIST_TITLE_RANK.awaitingApproval);
  });

  it("says an approved request is waiting for a download", () => {
    const status = watchlistTitleStatus(
      title({ request: { requestable: false, status: "approved" } }),
      NOW,
    );
    expect(status.badge).toBe("Approved");
    expect(status.badgeIcon).toBe("hourglass");
    expect(status.caption).toBe("Approved · waiting for a download");
  });

  it("offers a request only when the title is requestable", () => {
    expect(watchlistTitleStatus(title(), NOW)).toMatchObject({
      badge: "Not requested",
      caption: "Not requested",
      requestable: true,
      rank: WATCHLIST_TITLE_RANK.notRequested,
    });
    expect(
      watchlistTitleStatus(
        title({ request: { requestable: false, reason: "quota_exceeded" } }),
        NOW,
      ),
    ).toMatchObject({ caption: "Not requested · request limit reached", requestable: false });
  });

  it("flags titles TMDB lost as needing attention", () => {
    expect(watchlistTitleStatus(title({ status: "needs_review" }), NOW)).toMatchObject({
      badge: "Needs attention",
      badgeIcon: "alert",
      caption: "TMDB lists it twice",
      attention: true,
      requestable: false,
    });
    expect(watchlistTitleStatus(title({ status: "removed" }), NOW)).toMatchObject({
      badge: "Not on TMDB",
      caption: "No longer listed on TMDB",
      attention: true,
    });
    expect(watchlistTitleNeedsAttention({ status: "removed" })).toBe(true);
    expect(watchlistTitleNeedsAttention({ status: "some_future_value" })).toBe(false);
  });
});

describe("sortWatchlistTitlesSoonestFirst", () => {
  it("orders downloading, dated, awaiting approval, not requested, then attention", () => {
    const download = {
      phase: "downloading",
      percent: 10,
      downloads: 1,
      updated_at: NOW.toISOString(),
    };
    const titles = [
      title({ tmdb_id: 5, status: "removed" }),
      title({ tmdb_id: 4 }),
      title({ tmdb_id: 3, request: { requestable: false, status: "pending" } }),
      title({
        tmdb_id: 22,
        release_date: "2027-03-01",
        request: { requestable: false, status: "approved" },
      }),
      title({
        tmdb_id: 21,
        release_date: "2026-11-01",
        request: { requestable: false, status: "approved" },
      }),
      title({ tmdb_id: 1, request: { requestable: false, status: "downloading", download } }),
    ];
    expect(sortWatchlistTitlesSoonestFirst(titles, NOW).map((t) => t.tmdb_id)).toEqual([
      1, 21, 22, 3, 4, 5,
    ]);
  });

  it("puts the most recently added first within a group of the same date", () => {
    const titles = [
      title({ tmdb_id: 1, added_at: "2026-09-01T00:00:00Z" }),
      title({ tmdb_id: 2, added_at: "2026-09-20T00:00:00Z" }),
    ];
    expect(sortWatchlistTitlesSoonestFirst(titles, NOW).map((t) => t.tmdb_id)).toEqual([2, 1]);
  });
});

describe("parseWatchlistTab", () => {
  it("reads only the not-in-library value as the titles tab", () => {
    expect(parseWatchlistTab(WATCHLIST_NOT_IN_LIBRARY_TAB)).toBe("not-in-library");
    expect(parseWatchlistTab(null)).toBe("library");
    expect(parseWatchlistTab("anything")).toBe("library");
  });
});

describe("watchlistTitlesHint", () => {
  it("drops the requested sentence when watchlist requests are off", () => {
    expect(watchlistTitlesHint(true)).toBe(
      "These titles aren't in the library yet, so they can't be played. They've been requested for you. When one arrives, it moves to In your library and you get a notification.",
    );
    expect(watchlistTitlesHint(false)).toBe(
      "These titles aren't in the library yet, so they can't be played. When one arrives, it moves to In your library.",
    );
  });
});

describe("watchlistTitlesAvailable", () => {
  it("needs both requests and watchlist titles on", () => {
    expect(
      watchlistTitlesAvailable({ requests_enabled: true, watchlist_titles_supported: true }),
    ).toBe(true);
    expect(
      watchlistTitlesAvailable({ requests_enabled: false, watchlist_titles_supported: true }),
    ).toBe(false);
    expect(
      watchlistTitlesAvailable({ requests_enabled: true, watchlist_titles_supported: false }),
    ).toBe(false);
    expect(watchlistTitlesAvailable(undefined)).toBe(false);
  });
});

describe("showWatchlistAutoRequestControl", () => {
  const status = { requests_enabled: true, allowed: true, watchlist_titles_supported: true };

  it("shows the switch while watchlist adds request", () => {
    expect(showWatchlistAutoRequestControl({ ...status, watchlist_requests: true }, true)).toBe(
      true,
    );
  });

  it("keeps the switch for a profile that opted out, so it can opt back in", () => {
    expect(showWatchlistAutoRequestControl({ ...status, watchlist_requests: false }, false)).toBe(
      true,
    );
  });

  it("hides the switch when the server setting is off", () => {
    expect(showWatchlistAutoRequestControl({ ...status, watchlist_requests: false }, true)).toBe(
      false,
    );
  });

  it("hides the switch when the viewer cannot request or the server is older", () => {
    expect(
      showWatchlistAutoRequestControl(
        { ...status, allowed: false, watchlist_requests: true },
        true,
      ),
    ).toBe(false);
    expect(showWatchlistAutoRequestControl({ allowed: true, watchlist_requests: true }, true)).toBe(
      false,
    );
    expect(showWatchlistAutoRequestControl(undefined, false)).toBe(false);
    expect(
      showWatchlistAutoRequestControl(
        { ...status, requests_enabled: false, watchlist_requests: false },
        false,
      ),
    ).toBe(false);
  });
});
