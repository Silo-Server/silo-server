import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useRequestServerRestart } from "./serverRestart";
import { adminKeys } from "../keys";

const accepted = (status: string) =>
  new Response(JSON.stringify({ status, message: "synthetic", notified_sessions: 1 }), {
    status: 202,
    headers: { "Content-Type": "application/json" },
  });
function fixture() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3, retryDelay: 0 }, queries: { retry: false } },
  });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  setProfileId("profile-a");
  setProfileToken("pin-a");
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
});

it("posts the v2 restart once with an empty body and refreshes server status", async () => {
  const fetchMock = vi.fn().mockResolvedValue(accepted("already_requested"));
  vi.stubGlobal("fetch", fetchMock);
  const fx = fixture();
  const invalidate = vi.spyOn(fx.client, "invalidateQueries");
  const { result } = renderHook(useRequestServerRestart, fx);
  let outcome: Awaited<ReturnType<typeof result.current.mutateAsync>> | undefined;
  await act(async () => {
    outcome = await result.current.mutateAsync();
  });
  expect(outcome?.status).toBe("already_requested");
  expect(fetchMock).toHaveBeenCalledOnce();
  const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
  expect(String(url)).toBe("/api/v2/admin/server/restart");
  expect(init.method).toBe("POST");
  expect(init.body).toBe("{}");
  await waitFor(() =>
    expect(invalidate).toHaveBeenCalledWith({ queryKey: adminKeys.serverStatus() }),
  );
});

it.each(["401", "network"])(
  "never replays an uncertain %s restart under global retry3",
  async (failure) => {
    const fetchMock =
      failure === "network"
        ? vi.fn().mockRejectedValue(new Error("connection lost"))
        : vi.fn().mockResolvedValue(
            new Response(
              JSON.stringify({
                type: "https://silo.dev/problems/authentication_required",
                title: "Unauthorized",
                status: 401,
                detail: "Expired",
                instance: "synthetic",
              }),
              { status: 401, headers: { "Content-Type": "application/problem+json" } },
            ),
          );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useRequestServerRestart, fixture());
    await expect(result.current.mutateAsync({ reason: "config" })).rejects.toBeInstanceOf(Error);
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls[0]?.[1].body).toBe(JSON.stringify({ reason: "config" }));
  },
);

it("refuses a queued restart after authority replacement", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockResolvedValue(accepted("restart_requested"));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useRequestServerRestart, fixture());
  const pending = result.current.mutateAsync().catch((error: unknown) => error);
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => {
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  expect(await pending).toBeInstanceOf(Error);
  expect(fetchMock).not.toHaveBeenCalled();
});
