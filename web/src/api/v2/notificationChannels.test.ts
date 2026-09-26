import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority } from "./notifications";
import {
  getNotificationEmailPreferences,
  getNotificationDiscordPreferences,
  updateNotificationEmailPreferences,
  updateNotificationDiscordPreferences,
} from "./notificationChannels";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setRefreshToken("refresh");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

it("never refreshes or replays mode writes", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
    async () =>
      new Response(
        JSON.stringify({
          type: "https://example.invalid/problems/authentication_required",
          title: "Unauthorized",
          status: 401,
        }),
        { status: 401, headers: { "Content-Type": "application/problem+json" } },
      ),
  );
  vi.stubGlobal("fetch", fetch);
  for (const run of [updateNotificationEmailPreferences, updateNotificationDiscordPreferences]) {
    fetch.mockClear();
    await expect(run({ mode: "off" }, captureNotificationAuthority())).rejects.toMatchObject({
      status: 401,
    });
    expect(fetch).toHaveBeenCalledTimes(1);
  }
});

it("sends only the selected mode under captured authority", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    new Response(
      JSON.stringify({
        mode: "off",
        custom_email: "",
        pending_email: "",
        can_edit_address: true,
      }),
      { headers: { "Content-Type": "application/json" } },
    ),
  );
  vi.stubGlobal("fetch", fetch);
  await updateNotificationEmailPreferences({ mode: "off" }, captureNotificationAuthority());
  expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({ mode: "off" });
  expect(new Headers(fetch.mock.calls[0]![1]?.headers).get("X-Profile-Id")).toBe("owner");
});

it("rejects both channel reads after profile replacement", async () => {
  const context = captureNotificationAuthority();
  setProfileId("other");
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  await expect(getNotificationEmailPreferences(context)).rejects.toThrow();
  await expect(getNotificationDiscordPreferences(context)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});

it("discards channel data returned after account replacement", async () => {
  const context = captureNotificationAuthority();
  let finish!: (r: Response) => void;
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const pending = getNotificationEmailPreferences(context);
  setAccessToken("replacement");
  finish(
    new Response(JSON.stringify({ custom_email: "private@example.test" }), {
      headers: { "Content-Type": "application/json" },
    }),
  );
  await expect(pending).rejects.toThrow();
});
