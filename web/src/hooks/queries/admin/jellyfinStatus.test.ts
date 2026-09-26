import { applyJellyfinCompatOperationUpdate } from "@/components/RealtimeEventsProvider";
import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import {
  captureProfileRequestContext,
  setAccessToken,
  setRefreshToken,
  setProfileId,
} from "@/api/client";
import { useJellyfinCompatStatus } from "./settings";
function wrapper(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })) {
  return ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("admin");
  setRefreshToken(null);
  setProfileId("profile-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("reads configured compatibility and local installer progress through v2", async () => {
  const body = {
    enabled: true,
    api_state: "enabled",
    web_state: "installing",
    prerequisites: [],
    operation: { id: "local", kind: "install", state: "running", progress_percent: 30 },
  };
  const fetchMock = vi
    .fn<typeof fetch>()
    .mockResolvedValue(
      new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }),
    );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useJellyfinCompatStatus(), { wrapper: wrapper() });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toMatchObject({
    ...body,
    operation: { ...body.operation, started_at: "" },
  });
  expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v2/admin/jellyfin-compat/status");
});
it("rejects metadata decoded under a different account or profile", async () => {
  let finish!: (body: string) => void;
  let start!: () => void;
  const reading = new Promise<void>((resolve) => {
    start = resolve;
  });
  const response = new Response(null, { headers: { "Content-Type": "application/json" } });
  vi.spyOn(response, "text").mockImplementation(() => {
    start();
    return new Promise((resolve) => {
      finish = resolve;
    });
  });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  renderHook(() => useJellyfinCompatStatus(), { wrapper: wrapper(client) });
  await reading;
  const captured = client.getQueryCache().getAll()[0]!;
  await act(async () => {
    setProfileId("profile-b");
    finish(JSON.stringify({ status: "available" }));
  });
  await waitFor(() => expect(captured.state.status).toBe("error"));
  expect(captured.state.data).toBeUndefined();
});

it("updates the mounted status query from running realtime progress", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation(
      async () =>
        new Response(
          JSON.stringify({
            enabled: true,
            api_state: "enabled",
            web_state: "installing",
            prerequisites: [],
          }),
          { headers: { "Content-Type": "application/json" } },
        ),
    ),
  );
  const { result } = renderHook(() => useJellyfinCompatStatus(), { wrapper: wrapper(client) });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data?.web_state).toBe("installing");
  act(() =>
    applyJellyfinCompatOperationUpdate(
      client,
      {
        id: "install",
        kind: "install",
        state: "running",
        started_at: "2026-09-06T00:00:00.000Z",
        phase: "building",
        progress_percent: 60,
      },
      captureProfileRequestContext()!,
    ),
  );
  await waitFor(() => expect(result.current.data?.operation?.progress_percent).toBe(60));
  expect(result.current.data?.operation?.phase).toBe("building");
});
