import { createElement } from "react";
import { renderHook, act } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useTestNotificationWebhook } from "@/hooks/queries/notificationWebhooks";
import { useTestServerNotificationChannel } from "@/hooks/queries/admin/serverNotificationChannels";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { stubPendingFetch } from "./pendingFetch.test-support";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken("refresh");
  setProfileId("primary");
  setProfileToken(null);
});
afterEach(() => vi.unstubAllGlobals());

for (const useTest of [useTestNotificationWebhook, useTestServerNotificationChannel]) {
  it("blocks a second click before React publishes pending state", async () => {
    const { fetch, finish } = stubPendingFetch();
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
