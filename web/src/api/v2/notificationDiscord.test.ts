import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority } from "./notifications";
import { beginNotificationDiscordLink } from "./notificationDiscord";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken("refresh");
  setProfileId("primary");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

it("does not replay link initiation or navigate with a stale result", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    new Response(JSON.stringify({ status: 401, title: "Unauthorized" }), {
      status: 401,
      headers: { "Content-Type": "application/problem+json" },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  await expect(beginNotificationDiscordLink(captureNotificationAuthority())).rejects.toMatchObject({
    status: 401,
  });
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(String(fetch.mock.calls[0]![0])).toContain("/api/v2/notifications/discord/link/init");
  let finish!: (response: Response) => void;
  fetch.mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  const pending = beginNotificationDiscordLink(captureNotificationAuthority());
  setProfileId("other");
  finish(
    new Response(JSON.stringify({ url: "https://discord.com/oauth2/authorize?state=synthetic" }), {
      headers: { "Content-Type": "application/json" },
    }),
  );
  await expect(pending).rejects.toThrow();
});
