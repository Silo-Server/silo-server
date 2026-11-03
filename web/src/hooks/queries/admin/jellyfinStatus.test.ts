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
