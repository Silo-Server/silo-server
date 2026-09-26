import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import {
  captureNotificationAuthority,
  listNotifications,
  markAllNotificationsRead,
  markNotificationRead,
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
