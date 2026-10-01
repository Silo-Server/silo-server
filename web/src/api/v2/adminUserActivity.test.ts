import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setProfileId,
  setProfileToken,
  setRefreshToken,
  StaleApiRequestContextError,
} from "@/api/client";
import {
  getAdminRequestUsage,
  getAdminUserDownloadSummary,
  getAdminUserWatchSummary,
  listAdminUserDevices,
  listAdminUserLiveSessions,
  listAdminUserPlays,
  listAdminUserProfileActivity,
  listAllAdminUserDownloadSubscriptions,
  listAllAdminUserDownloads,
} from "./adminUserActivity";

function response(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
}
function url(fetch: ReturnType<typeof vi.fn>, call = 0) {
  return new URL(String(fetch.mock.calls[call]![0]), "http://localhost");
}
function ctx() {
  return captureProfileRequestContext()!;
}
const done = { has_more: false };

const play = {
  session_id: "s1",
  user_id: "7",
  username: "taylor",
  profile_id: "p1",
  profile_name: "Main",
  media_item_id: "ep-1",
  media_file_id: "3",
  media_title: "Hello",
  media_type: "episode",
  series_title: "Severance",
  season_number: 2,
  episode_number: 4,
  play_method: "direct",
  started_at: "2026-09-28T10:00:00.000Z",
  ended_at: "2026-09-28T11:00:00.000Z",
  watched_seconds: 3000,
  duration_seconds: null,
  completed: false,
};
const download = {
  id: "11",
  profile_id: "p1",
  device_id: "dev-a",
  content_id: "series-1",
  episode_id: "ep-1",
  title: "Severance",
  media_type: "series",
  episode: { season_number: 2, episode_number: 1, title: "Hello" },
  status: "ready",
  quality: "original",
  effective_quality: "original",
  delivery_format: "original",
  target_bitrate_kbps: 0,
  file_size: 1_000,
  created_at: "2026-09-28T10:00:00.000Z",
  updated_at: "2026-09-28T10:00:00.000Z",
  completed_at: null,
  status_event_at: null,
};
const subscription = {
  id: "5",
  profile_id: "p1",
  device_id: "dev-a",
  series_id: "series-1",
  series_title: "Severance",
  mode: "all",
  season_numbers: [],
  target_season: null,
  delete_watched: true,
  max_storage_bytes: 0,
  active: true,
  on_device: 6,
  in_progress: 0,
  removed_episodes: 2,
  created_at: "2026-09-28T10:00:00.000Z",
  updated_at: "2026-09-28T10:00:00.000Z",
};

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setRefreshToken("refresh");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

it("addresses each account read by its string id and maps nullables", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
    const path = new URL(String(input), "http://localhost").pathname;
    if (path.endsWith("/devices"))
      return response({
        items: [
          {
            device_id: "dev-a",
            device_name: "Apple TV",
            device_platform: "tvOS",
            last_seen_at: null,
            last_updated: "2026-09-28T10:00:00.000Z",
            override_count: 2,
            profiles: [
              { profile_id: "p1", profile_name: "", override_count: 2, last_seen_at: null },
            ],
          },
        ],
        page: done,
      });
    if (path.endsWith("/profiles"))
      return response({ items: [{ id: "p1", name: "Main", last_seen_at: null }], page: done });
    if (path.endsWith("/watch-summary"))
      return response({
        days: 7,
        profile_id: "p1",
        since: "2026-09-22T10:00:00.000Z",
        plays: 4,
        completed_plays: 2,
        watched_seconds: 900,
        last_played_at: null,
      });
    if (path.endsWith("/usage"))
      return response({
        requests_enabled: true,
        allowed: false,
        unlimited: false,
        used: 3,
        max_requests: 10,
        window_days: 7,
        window_start: "2026-09-22T10:00:00.000Z",
        remaining: 7,
        auto_approve: false,
      });
    if (path.endsWith("/downloads/summary"))
      return response({
        total: 3,
        completed: 1,
        in_progress: 2,
        failed: 0,
        revoked: 0,
        total_bytes: 3000,
        devices: 1,
        monitored_series: 1,
      });
    throw new Error(`unexpected ${path}`);
  });
  vi.stubGlobal("fetch", fetch);

  const devices = await listAdminUserDevices(7, ctx());
  expect(url(fetch, 0).pathname).toBe("/api/v2/admin/users/7/devices");
  expect(devices[0]).toMatchObject({ last_seen_at: null, override_count: 2 });
  expect(devices[0]!.profiles[0]).toEqual({
    profile_id: "p1",
    profile_name: "",
    override_count: 2,
    last_seen_at: null,
  });

  expect(await listAdminUserProfileActivity(7, ctx())).toEqual([
    { id: "p1", name: "Main", last_seen_at: null },
  ]);
  expect(url(fetch, 1).pathname).toBe("/api/v2/admin/users/7/profiles");

  const summary = await getAdminUserWatchSummary(7, { days: 7, profileId: "p1" }, ctx());
  expect(url(fetch, 2).pathname).toBe("/api/v2/admin/users/7/watch-summary");
  expect(url(fetch, 2).searchParams.get("days")).toBe("7");
  expect(url(fetch, 2).searchParams.get("profile_id")).toBe("p1");
  expect(summary).toMatchObject({ plays: 4, last_played_at: null });

  expect(await getAdminRequestUsage(7, ctx())).toMatchObject({ allowed: false, used: 3 });
  expect(url(fetch, 3).pathname).toBe("/api/v2/admin/request-users/7/usage");

  expect(await getAdminUserDownloadSummary(7, ctx())).toMatchObject({ total: 3 });
  expect(url(fetch, 4).pathname).toBe("/api/v2/admin/users/7/downloads/summary");
});

it("filters plays by account, profile and ended_after derived from days", async () => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(new Date("2026-09-30T12:00:00.000Z"));
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockImplementation(async (input) =>
      new URL(String(input), "http://localhost").searchParams.has("cursor")
        ? response({ items: [play], page: done })
        : response({ items: [play], page: { has_more: true, next_cursor: "c2" } }),
    );
  vi.stubGlobal("fetch", fetch);

  const page = await listAdminUserPlays({ userId: 7, profileId: "p1", days: 30, limit: 25 }, ctx());
  const query = url(fetch).searchParams;
  expect(url(fetch).pathname).toBe("/api/v2/admin/playback-history");
  expect(query.get("user_id")).toBe("7");
  expect(query.get("profile_id")).toBe("p1");
  expect(query.get("limit")).toBe("25");
  expect(query.get("ended_after")).toBe("2026-08-31T12:00:00.000Z");
  expect(page.nextCursor).toBe("c2");
  expect(page.items[0]).toMatchObject({
    series_title: "Severance",
    season_number: 2,
    episode_number: 4,
    duration_seconds: null,
  });

  await listAdminUserPlays(
    { userId: 7, days: 30, limit: 25, cursor: "c2", endedAfter: "2026-08-01T00:00:00.000Z" },
    ctx(),
  );
  expect(url(fetch, 1).searchParams.get("cursor")).toBe("c2");
  expect(url(fetch, 1).searchParams.get("ended_after")).toBe("2026-08-01T00:00:00.000Z");
  expect(url(fetch, 1).searchParams.has("profile_id")).toBe(false);
});

it.each([
  ["has_more without a cursor", { has_more: true }],
  ["a cursor on the last page", { has_more: false, next_cursor: "x" }],
  ["the cursor it was asked for", { has_more: true, next_cursor: "c1" }],
])("rejects a play page with %s", async (_name, page) => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>().mockResolvedValue(response({ items: [play], page })),
  );
  await expect(
    listAdminUserPlays({ userId: 7, days: 30, limit: 25, cursor: "c1" }, ctx()),
  ).rejects.toThrow("Invalid watch history page. Reload the page.");
});

it("walks every download page and refuses a repeated cursor", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValueOnce(
      response({ items: [download], page: { has_more: true, next_cursor: "a" } }),
    )
    .mockResolvedValueOnce(
      response({ items: [{ ...download, id: "12" }], page: { has_more: false } }),
    );
  vi.stubGlobal("fetch", fetch);
  const { items: rows, truncated } = await listAllAdminUserDownloads(7, ctx());
  expect(rows.map((row) => row.id)).toEqual(["11", "12"]);
  expect(truncated).toBe(false);
  expect(url(fetch, 0).pathname).toBe("/api/v2/admin/users/7/downloads");
  expect(url(fetch, 0).searchParams.get("limit")).toBe("200");
  expect(url(fetch, 1).searchParams.get("cursor")).toBe("a");
  expect(rows[0]!.completed_at).toBeNull();

  fetch.mockReset();
  fetch
    .mockResolvedValueOnce(
      response({ items: [download], page: { has_more: true, next_cursor: "a" } }),
    )
    .mockResolvedValueOnce(
      response({ items: [download], page: { has_more: true, next_cursor: "a" } }),
    );
  await expect(listAllAdminUserDownloads(7, ctx())).rejects.toThrow(
    "Invalid download page. Reload the page.",
  );
});

it("keeps the downloads it read when the account has more than the page cap", async () => {
  let page = 0;
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () => {
    page += 1;
    return response({
      items: [{ ...download, id: String(page) }],
      page: { has_more: true, next_cursor: `c${page}` },
    });
  });
  vi.stubGlobal("fetch", fetch);
  const { items, truncated } = await listAllAdminUserDownloads(7, ctx());
  expect(truncated).toBe(true);
  expect(items).toHaveLength(25);
  expect(fetch).toHaveBeenCalledTimes(25);
});

it("keeps season numbers an array and maps monitors", async () => {
  const fetch = vi
    .fn<typeof globalThis.fetch>()
    .mockResolvedValue(response({ items: [subscription], page: done }));
  vi.stubGlobal("fetch", fetch);
  const {
    items: [row],
  } = await listAllAdminUserDownloadSubscriptions(7, ctx());
  expect(url(fetch).pathname).toBe("/api/v2/admin/users/7/download-subscriptions");
  expect(row).toMatchObject({ season_numbers: [], target_season: null, removed_episodes: 2 });
});

it("rejects a bounded device list that claims more pages", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn<typeof fetch>()
      .mockResolvedValue(response({ items: [], page: { has_more: true, next_cursor: "more" } })),
  );
  await expect(listAdminUserDevices(7, ctx())).rejects.toThrow(
    "Invalid device page. Reload the page.",
  );
});

it("reads live sessions for one account and projects numeric ids", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    response({
      items: [
        {
          session_id: "live-1",
          user_id: "7",
          media_file_id: "3",
          requested_media_file_id: "3",
          profile_id: "p1",
          media_title: "Hello",
          play_method: "direct",
        },
      ],
      page: done,
    }),
  );
  vi.stubGlobal("fetch", fetch);
  const sessions = await listAdminUserLiveSessions(7, ctx());
  expect(url(fetch).pathname).toBe("/api/v2/admin/sessions");
  expect(url(fetch).searchParams.get("user_id")).toBe("7");
  expect(url(fetch).searchParams.get("limit")).toBe("100");
  expect(sessions[0]).toMatchObject({ session_id: "live-1", user_id: 7, media_file_id: 3 });
});

it("keeps the live sessions it read past the page cap", async () => {
  let page = 0;
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () => {
    page += 1;
    return response({
      items: [
        {
          session_id: `live-${page}`,
          user_id: "7",
          media_file_id: "3",
          requested_media_file_id: "3",
          profile_id: "p1",
          media_title: "Hello",
          play_method: "direct",
        },
      ],
      page: { has_more: true, next_cursor: `c${page}` },
    });
  });
  vi.stubGlobal("fetch", fetch);
  const sessions = await listAdminUserLiveSessions(7, ctx());
  expect(sessions).toHaveLength(10);
  expect(fetch).toHaveBeenCalledTimes(10);
});

it("refuses to deliver a read after the admin authority changed", async () => {
  const captured = ctx();
  const fetch = vi.fn<typeof globalThis.fetch>(async () => {
    setProfileId("someone-else");
    return response({ items: [], page: done });
  });
  vi.stubGlobal("fetch", fetch);
  await expect(listAdminUserDevices(7, captured)).rejects.toBeInstanceOf(
    StaleApiRequestContextError,
  );
  // A request under an authority that already changed is never sent.
  fetch.mockClear();
  await expect(getAdminUserDownloadSummary(7, captured)).rejects.toBeInstanceOf(
    StaleApiRequestContextError,
  );
  expect(fetch).not.toHaveBeenCalled();
});
