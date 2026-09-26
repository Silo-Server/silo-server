import { createElement } from "react";
import { renderHook, act } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useCreateNotificationWebhook } from "@/hooks/queries/notificationWebhooks";
import { useCreateServerNotificationChannel } from "@/hooks/queries/admin/serverNotificationChannels";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";

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
  it("does not replay creation after401 even with global retries", async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(
      new Response(JSON.stringify({ status: 401, title: "Unauthorized" }), {
        status: 401,
        headers: { "Content-Type": "application/problem+json" },
      }),
    );
    vi.stubGlobal("fetch", fetch);
    const client = new QueryClient({ defaultOptions: { mutations: { retry: 3 } } });
    const { result, unmount } = renderHook(() => useCreate(), {
      wrapper: ({ children }) => createElement(QueryClientProvider, { client }, children),
    });
    await act(async () => {
      await expect(
        result.current.mutateAsync({ name: "fixture", url: "https://example.test/hook" }),
      ).rejects.toMatchObject({ status: 401 });
    });
    expect(fetch).toHaveBeenCalledTimes(1);
    unmount();
    client.clear();
  });
  it("suppresses a one-time secret after authority changes", async () => {
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
