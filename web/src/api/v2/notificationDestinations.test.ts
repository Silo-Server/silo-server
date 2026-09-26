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
it("rejects stale authority before reading destination metadata", async () => {
  const context = captureNotificationAuthority();
  setProfileId("replacement");
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  await expect(listNotificationServerChannels(context)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
it("discards destination pages returned after account replacement", async () => {
  const context = captureNotificationAuthority();
  let finish!: (r: Response) => void;
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const pending = listNotificationWebPushSubscriptions(context);
  setAccessToken("replacement");
  finish(json({ items: [], page: { has_more: false } }));
  await expect(pending).rejects.toThrow();
});

it("deletes the exact subscription once without authentication replay", async () => {
  for (const status of [204, 401, 403, 500]) {
    const fetch = vi
      .fn<typeof globalThis.fetch>()
      .mockResolvedValue(new Response(null, { status }));
    vi.stubGlobal("fetch", fetch);
    const result = deleteNotificationWebPushSubscription("row-one", captureNotificationAuthority());
    if (status === 204) await result;
    else await expect(result).rejects.toThrow();
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(String(fetch.mock.calls[0]![0])).toContain(
      "/api/v2/notifications/web-push/subscriptions/row-one",
    );
    expect(fetch.mock.calls[0]![1]?.method).toBe("DELETE");
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
