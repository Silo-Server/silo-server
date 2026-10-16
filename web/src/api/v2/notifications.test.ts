import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  captureNotificationAuthority,
  listNotifications,
  listNotificationSeries,
  markAllNotificationsRead,
  markNotificationRead,
  setNotificationSeriesMuted,
  updateNotificationPreferences,
} from "./notifications";
const row = {
  id: "delivery-1",
  type: "episode.available",
  profile_id: "owner",
  library_id: "7",
  reason_flags: {},
  created_at: "2026-01-01T00:00:00.000Z",
  read_at: null,
};
function response(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": status >= 400 ? "application/problem+json" : "application/json" },
  });
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setRefreshToken("refresh");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());
it("preserves the server cutoff and serializes it exactly for mark-all", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(
      response({ items: [row], page: { has_more: false }, read_cutoff: "signed-observed-cutoff" }),
    )
    .mockResolvedValueOnce(new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  const page = await listNotifications("all");
  expect(page.notifications[0]?.library_id).toBe(7);
  await markAllNotificationsRead(page.read_cutoff);
  expect(JSON.parse(String(fetch.mock.calls[1]![1]?.body))).toEqual({
    through: "signed-observed-cutoff",
  });
  expect(new Headers(fetch.mock.calls[1]![1]?.headers).get("X-Profile-Id")).toBe("owner");
});
it("keeps the follower flag so a followed title is not described as the viewer's request", async () => {
  const fulfilled = {
    ...row,
    type: "request.fulfilled",
    reason_flags: { request_id: "9", media_type: "movie", tmdb_id: 10378, follower: true },
  };
  vi.stubGlobal(
    "fetch",
    vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(
        response({ items: [fulfilled], page: { has_more: false }, read_cutoff: "cutoff" }),
      ),
  );
  const page = await listNotifications("all");
  expect(page.notifications[0]?.reason_flags).toEqual({
    request_id: "9",
    media_type: "movie",
    tmdb_id: 10378,
    follower: true,
  });
});
it("refuses a missing cutoff or malformed page instead of marking the current inbox", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  await expect(markAllNotificationsRead("")).rejects.toThrow("Reload");
  expect(fetch).not.toHaveBeenCalled();
  for (const page of [
    { has_more: true },
    { has_more: true, next_cursor: 7 },
    { has_more: false },
  ]) {
    fetch.mockResolvedValue(response({ items: [row], page }));
    await expect(listNotifications("all")).rejects.toThrow("page");
  }
});
it("never refreshes or replays inbox and preference mutations", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () =>
    response(
      {
        type: "https://example.invalid/problems/authentication_required",
        title: "Unauthorized",
        status: 401,
      },
      401,
    ),
  );
  vi.stubGlobal("fetch", fetch);
  for (const run of [
    () => markNotificationRead("delivery-1"),
    () => markAllNotificationsRead("cutoff"),
    () => updateNotificationPreferences({ enabled: false }),
    () => setNotificationSeriesMuted("series:bear", true),
  ]) {
    fetch.mockClear();
    await expect(run()).rejects.toMatchObject({ status: 401 });
    expect(fetch).toHaveBeenCalledTimes(1);
  }
});
it("rejects a captured authority after profile replacement", async () => {
  const authority = captureNotificationAuthority();
  setProfileId("other");
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  await expect(markAllNotificationsRead("cutoff", authority)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
it("preserves explicit false preferences and omits response-only profile identity", async () => {
  const fetcher = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ profile_id: "owner", enabled: false }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetcher);
  await updateNotificationPreferences({
    profile_id: "ignored",
    enabled: false,
    notify_favorites: false,
  });
  expect(JSON.parse(String(fetcher.mock.calls[0]![1]?.body))).toEqual({
    enabled: false,
    notify_favorites: false,
  });
});
it("pages notified series by status and rejects a page that repeats its cursor", async () => {
  const series = {
    item: {
      content_id: "series:bear",
      title: "The Bear",
      type: "series",
      status: "matched",
      genres: [],
      keywords: [],
    },
    reasons: { favorite: true, watchlist: false, continue_watching: false, next_up: false },
    active_reasons: { favorite: true, watchlist: false, continue_watching: false, next_up: false },
    notifies: true,
  };
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(
      response({ items: [series], page: { has_more: true, next_cursor: "next" } }),
    )
    .mockResolvedValueOnce(response({ items: [], page: { has_more: true, next_cursor: "next" } }));
  vi.stubGlobal("fetch", fetch);
  const page = await listNotificationSeries("inactive", {
    q: "bear",
    reason: "favorite",
    sort: "-latest_episode_added",
  });
  expect(page).toEqual({ series: [series], next_cursor: "next" });
  const url = new URL(String(fetch.mock.calls[0]![0]), "http://silo.test");
  expect(url.pathname).toBe("/api/v2/notifications/series");
  expect(Object.fromEntries(url.searchParams)).toMatchObject({
    status: "inactive",
    q: "bear",
    reason: "favorite",
    sort: "-latest_episode_added",
  });
  await expect(listNotificationSeries("inactive", {}, "next")).rejects.toThrow(
    "Invalid series page",
  );
});
it("mutes with PUT and unmutes with DELETE on the series' mute", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementation(async () => new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  await setNotificationSeriesMuted("series:bear", true);
  await setNotificationSeriesMuted("series:bear", false);
  const calls = fetch.mock.calls.map(([input, init]) => [
    new URL(String(input), "http://silo.test").pathname,
    init?.method,
  ]);
  expect(calls).toEqual([
    ["/api/v2/notifications/series/series%3Abear/mute", "PUT"],
    ["/api/v2/notifications/series/series%3Abear/mute", "DELETE"],
  ]);
});
