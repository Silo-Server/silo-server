import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority } from "./notifications";
import { clearNotificationRelay, registerNotificationRelay } from "./notificationRelay";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken("refresh");
  setProfileId("primary");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

it("never refreshes or replays a relay register or clear after 401", async () => {
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
  for (const run of [
    () => registerNotificationRelay("https://relay.example.test", captureNotificationAuthority()),
    () => clearNotificationRelay(captureNotificationAuthority()),
  ]) {
    fetch.mockClear();
    await expect(run()).rejects.toMatchObject({ status: 401 });
    expect(fetch).toHaveBeenCalledTimes(1);
  }
});

it("sends the captured authority and only the relay URL", async () => {
  const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
    new Response(JSON.stringify({ api_key_configured: true }), {
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  await registerNotificationRelay("https://relay.example.test", captureNotificationAuthority());
  expect(String(fetch.mock.calls[0]![0])).toContain(
    "/api/v2/admin/notifications/push/relay/register",
  );
  expect(JSON.parse(String(fetch.mock.calls[0]![1]?.body))).toEqual({
    relay_url: "https://relay.example.test",
  });
  expect(new Headers(fetch.mock.calls[0]![1]?.headers).get("X-Profile-Id")).toBe("primary");
});

it("refuses a stale captured administrator before dispatch", async () => {
  const context = captureNotificationAuthority();
  setProfileId("other");
  const fetch = vi.fn<typeof globalThis.fetch>();
  vi.stubGlobal("fetch", fetch);
  await expect(clearNotificationRelay(context)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});

it("does not publish a relay response after account replacement", async () => {
  const context = captureNotificationAuthority();
  let finish!: (response: Response) => void;
  const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.stubGlobal("fetch", fetch);
  const pending = registerNotificationRelay("https://relay.example.test", context);
  setAccessToken("other-admin");
  finish(
    new Response(JSON.stringify({ deployment_id: "old-deployment" }), {
      headers: { "Content-Type": "application/json" },
    }),
  );
  await expect(pending).rejects.toThrow();
});
