import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useSaveAdminDashboardLayout, useResetAdminDashboardLayout } from "./dashboardLayout";
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
it("captures reset authority before offline queueing and sends once", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockResolvedValue(response());
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useResetAdminDashboardLayout, fixture());
  act(() => result.current.mutate());
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => onlineManager.setOnline(true));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/v2/admin/dashboard/layout");
  expect(fetchMock.mock.calls[0]?.[1].method).toBe("DELETE");
  expect(fetchMock.mock.calls[0]?.[1].body).toBeUndefined();
});
it("refuses a queued deletion after authority replacement", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useResetAdminDashboardLayout, fixture());
  act(() => result.current.mutate());
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
    const { result } = renderHook(useResetAdminDashboardLayout, fixture());
    act(() => result.current.mutate());
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
  const { result } = renderHook(useResetAdminDashboardLayout, { wrapper });
  act(() => result.current.mutate());
  await waitFor(() => expect(release).toBeTypeOf("function"));
  act(() => setProfileId("profile-b"));
  await act(async () => release(response()));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(invalidate).not.toHaveBeenCalled();
});

it("keeps reset behind an in-flight legacy save in the shared mutation scope", async () => {
  let finish!: () => void;
  const writes: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((_url: unknown, init: RequestInit) => {
      writes.push(init.method!);
      if (init.method === "PUT")
        return new Promise<Response>((resolve) => {
          finish = () => resolve(response());
        });
      return Promise.resolve(response());
    }),
  );
  const { result } = renderHook(
    () => ({ save: useSaveAdminDashboardLayout(), reset: useResetAdminDashboardLayout() }),
    fixture(),
  );
  act(() => {
    result.current.save.mutate({ version: 1, entries: [] }, '"A"');
    result.current.reset.mutate();
  });
  await waitFor(() => expect(writes).toEqual(["PUT"]));
  await act(async () => finish());
  await waitFor(() => expect(result.current.reset.isSuccess).toBe(true));
  expect(writes).toEqual(["PUT", "DELETE"]);
});
