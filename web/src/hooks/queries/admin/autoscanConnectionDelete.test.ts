import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useDeleteAutoscanConnection } from "../useAutoscan";
const response = () => new Response(null, { status: 204 });
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
it("captures the deletion target before offline queueing and sends once", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockResolvedValue(response());
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useDeleteAutoscanConnection, fixture());
  let id = "connection-a";
  act(() => result.current.mutate(id));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  id = "connection-b";
  act(() => onlineManager.setOnline(true));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
    "/api/v2/admin/autoscan/connections/connection-a",
  );
  expect(fetchMock.mock.calls[0]?.[1].method).toBe("DELETE");
  expect(fetchMock.mock.calls[0]?.[1].body).toBeUndefined();
});
it("refuses a queued deletion after authority replacement", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useDeleteAutoscanConnection, fixture());
  act(() => result.current.mutate("connection-a"));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => {
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});
it.each(["401", "network"])(
  "never replays uncertain %s connection deletion under global retry3",
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
    const { result } = renderHook(useDeleteAutoscanConnection, fixture());
    act(() => result.current.mutate("connection-a"));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
  },
);
it("does not invalidate another authority after late deletion acknowledgement", async () => {
  let release!: (response: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          release = resolve;
        }),
    ),
  );
  const { client, wrapper } = fixture();
  const invalidate = vi.spyOn(client, "invalidateQueries");
  const { result } = renderHook(useDeleteAutoscanConnection, { wrapper });
  act(() => result.current.mutate("connection-a"));
  await waitFor(() => expect(release).toBeTypeOf("function"));
  act(() => setProfileId("profile-b"));
  await act(async () => release(response()));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(invalidate).not.toHaveBeenCalled();
});
