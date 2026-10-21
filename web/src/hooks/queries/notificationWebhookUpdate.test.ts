import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useUpdateNotificationWebhook } from "./notificationWebhooks";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("account");
  setProfileId("owner");
  setProfileToken(null);
});
afterEach(() => {
  onlineManager.setOnline(true);
  cleanup();
  vi.unstubAllGlobals();
});

it("preserves the invocation body and observed validator while paused", async () => {
  const client = new QueryClient();
  const fetch = vi.fn().mockResolvedValue(
    new Response(JSON.stringify({ id: "row-one", etag: '"next"' }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  const { result, rerender } = renderHook(() => useUpdateNotificationWebhook(), {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  });
  onlineManager.setOnline(false);
  const input = { id: "row-one", etag: '"original"', name: "original name", enabled: false };
  act(() => {
    result.current.mutate(input);
  });
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  input.etag = '"replacement"';
  input.name = "replacement name";
  input.enabled = true;
  rerender();
  await act(async () => {
    onlineManager.setOnline(true);
    await client.resumePausedMutations();
  });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetch).toHaveBeenCalledTimes(1);
  const request = fetch.mock.calls[0]![1];
  expect(new Headers(request.headers).get("If-Match")).toBe('"original"');
  expect(JSON.parse(request.body)).toEqual({ name: "original name", enabled: false });
  client.clear();
});
