import { createElement } from "react";
import { renderHook, act } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useCreateNotificationWebhook } from "@/hooks/queries/notificationWebhooks";
import { useCreateServerNotificationChannel } from "@/hooks/queries/admin/serverNotificationChannels";
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

for (const useTest of [useCreateNotificationWebhook, useCreateServerNotificationChannel]) {
  it("blocks a second click before React publishes pending state", async () => {
    const { fetch, finish } = stubPendingFetch();
    const client = new QueryClient();
    const { result, unmount } = renderHook(() => useTest(), {
      wrapper: ({ children }) => createElement(QueryClientProvider, { client }, children),
    });
    let first!: Promise<unknown>;
    await act(async () => {
      first = result.current.mutateAsync({ name: "fixture", url: "https://example.test/hook" });
      await expect(
        result.current.mutateAsync({ name: "fixture", url: "https://example.test/hook" }),
      ).rejects.toThrow("already in progress");
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    await act(async () => {
      finish(
        new Response(
          JSON.stringify({
            id: "created",
            name: "fixture",
            type: "generic",
            url_host: "example.test",
            signing_secret: "once",
          }),
          {
            headers: { "Content-Type": "application/json" },
          },
        ),
      );
      await first;
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    unmount();
    client.clear();
  });
}

for (const useCreate of [useCreateNotificationWebhook, useCreateServerNotificationChannel]) {
  it("suppresses a one-time secret after authority changes", async () => {
    const { finish } = stubPendingFetch();
    const client = new QueryClient();
    const { result, unmount } = renderHook(() => useCreate(), {
      wrapper: ({ children }) => createElement(QueryClientProvider, { client }, children),
    });
    let first!: Promise<unknown>;
    await act(async () => {
      first = result.current.mutateAsync({ name: "fixture", url: "https://example.test/hook" });
    });
    setProfileId("replacement");
    await act(async () => {
      finish(
        new Response(
          JSON.stringify({
            id: "created",
            name: "fixture",
            type: "generic",
            url_host: "example.test",
            signing_secret: "once",
          }),
          { status: 201, headers: { "Content-Type": "application/json" } },
        ),
      );
      await expect(first).rejects.toThrow();
    });
    unmount();
    client.clear();
  });
}
