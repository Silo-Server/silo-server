import { createElement } from "react";
import { renderHook, act } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useTestNotificationWebhook } from "@/hooks/queries/notificationWebhooks";
import { useTestServerNotificationChannel } from "@/hooks/queries/admin/serverNotificationChannels";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { captureNotificationAuthority } from "./notifications";
import { testNotificationDestination } from "./notificationDestinationTests";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken("refresh");
  setProfileId("primary");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

for (const kind of ["webhook", "server-channel"] as const) {
  const run = (authority: ReturnType<typeof captureNotificationAuthority>) =>
    testNotificationDestination(kind, "destination", authority);
  it(`${kind}: does not refresh or replay a failed test send`, async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      new Response(JSON.stringify({ status: 401, title: "Unauthorized" }), {
        status: 401,
        headers: { "Content-Type": "application/problem+json" },
      }),
    );
    vi.stubGlobal("fetch", fetch);
    await expect(run(captureNotificationAuthority())).rejects.toMatchObject({
      status: 401,
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    expect(String(fetch.mock.calls[0]![0])).toContain(
      kind === "webhook"
        ? "/api/v2/notifications/webhooks/destination/test"
        : "/api/v2/admin/notifications/server-channels/destination/test",
    );
    expect(new Headers(fetch.mock.calls[0]![1]?.headers).get("X-Profile-Id")).toBe("primary");
  });

  it("refuses dispatch with stale captured authority", async () => {
    const authority = captureNotificationAuthority();
    setProfileId("other");
    const fetch = vi.fn<typeof globalThis.fetch>();
    vi.stubGlobal("fetch", fetch);
    await expect(run(authority)).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
  });

  it("refuses to publish a result after the account changes", async () => {
    let finish!: (response: Response) => void;
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof globalThis.fetch>().mockImplementation(
        () =>
          new Promise((resolve) => {
            finish = resolve;
          }),
      ),
    );
    const pending = run(captureNotificationAuthority());
    setAccessToken("other-admin");
    finish(
      new Response(
        JSON.stringify({ ok: true, duration_ms: 1, message: "Connected as fixture-bot" }),
        { headers: { "Content-Type": "application/json" } },
      ),
    );
    await expect(pending).rejects.toThrow();
  });
}

for (const useTest of [useTestNotificationWebhook, useTestServerNotificationChannel]) {
  it("blocks a second click before React publishes pending state", async () => {
    let finish!: (response: Response) => void;
    const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetch);
    const client = new QueryClient();
    const { result, unmount } = renderHook(() => useTest(), {
      wrapper: ({ children }) => createElement(QueryClientProvider, { client }, children),
    });
    let first!: Promise<unknown>;
    await act(async () => {
      first = result.current.mutateAsync("destination");
      await expect(result.current.mutateAsync("destination")).rejects.toThrow(
        "already in progress",
      );
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    await act(async () => {
      finish(
        new Response(JSON.stringify({ ok: false, http_status: 429, duration_ms: 2 }), {
          headers: { "Content-Type": "application/json" },
        }),
      );
      await first;
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    unmount();
    client.clear();
  });
}
