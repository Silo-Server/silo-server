import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority } from "./notifications";
import {
  listNotificationWebPushSubscriptions,
  deleteNotificationWebPushSubscription,
  listNotificationWebhooks,
  listNotificationServerChannels,
} from "./notificationDestinations";
const json = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken("refresh");
  setProfileId("primary");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

it("drains each destination list using the signed cursor and captured profile", async () => {
  for (const list of [
    listNotificationWebPushSubscriptions,
    listNotificationWebhooks,
    listNotificationServerChannels,
  ]) {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValueOnce(
        json({ items: [{ id: "one" }], page: { has_more: true, next_cursor: "signed-next" } }),
      )
      .mockResolvedValueOnce(json({ items: [{ id: "two" }], page: { has_more: false } }));
    vi.stubGlobal("fetch", fetch);
    expect(await list(captureNotificationAuthority())).toEqual([{ id: "one" }, { id: "two" }]);
    expect(String(fetch.mock.calls[1]![0])).toContain("cursor=signed-next");
    expect(new Headers(fetch.mock.calls[1]![1]?.headers).get("X-Profile-Id")).toBe("primary");
  }
});
it("rejects repeated or missing continuations without returning partial data", async () => {
  for (const page of [
    { has_more: true, next_cursor: "same" },
    { has_more: true },
    { has_more: "yes" },
    undefined,
  ]) {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockImplementation(async () => json({ items: [{ id: "one" }], page }));
    vi.stubGlobal("fetch", fetch);
    await expect(listNotificationWebhooks(captureNotificationAuthority())).rejects.toThrow();
    expect(fetch.mock.calls.length).toBeLessThanOrEqual(2);
  }
});
it("refuses stale deletion dispatch and stale receipt after PIN replacement", async () => {
  const context = captureNotificationAuthority();
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async () => {
    setProfileToken("replacement");
    return new Response(null, { status: 204 });
  });
  vi.stubGlobal("fetch", fetch);
  await expect(deleteNotificationWebPushSubscription("row", context)).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
  await expect(deleteNotificationWebPushSubscription("row", context)).rejects.toThrow();
  expect(fetch).toHaveBeenCalledTimes(1);
});
