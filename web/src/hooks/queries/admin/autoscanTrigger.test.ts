import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useTriggerAutoscan } from "../useAutoscan";
const response = () =>
  new Response(
    JSON.stringify({ key: "autoscan_poll", state: "running", execution_scope: "process" }),
    { headers: { "Content-Type": "application/json" } },
  );
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

it("queued click starts one process poll command without body", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockResolvedValue(response());
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useTriggerAutoscan, fixture());
  act(() => result.current.mutate());
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
  act(() => onlineManager.setOnline(true));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/v2/admin/autoscan/trigger");
  expect(fetchMock.mock.calls[0]?.[1].method).toBe("POST");
  expect(fetchMock.mock.calls[0]?.[1].body).toBeUndefined();
  expect(result.current.data).toMatchObject({
    key: "autoscan_poll",
    execution_scope: "process",
    state: "running",
  });
});
it("PIN replacement before queue release refuses dispatch", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useTriggerAutoscan, fixture());
  act(() => result.current.mutate());
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => {
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});
it.each([401, 409, "network"])(
  "never automatically replays %s under global retry3",
  async (failure) => {
    const fetchMock =
      failure === "network"
        ? vi.fn().mockRejectedValue(new Error("private"))
        : vi.fn().mockImplementation(() =>
            Promise.resolve(
              new Response(
                JSON.stringify({
                  type: "https://silo.dev/problems/conflict",
                  status: failure,
                  title: "Conflict",
                }),
                {
                  status: Number(failure),
                  headers: { "Content-Type": "application/problem+json" },
                },
              ),
            ),
          );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(useTriggerAutoscan, fixture());
    act(() => result.current.mutate());
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
  },
);
it("late process acknowledgement cannot invalidate replacement profile state", async () => {
  let release!: (r: Response) => void;
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
  const { result } = renderHook(useTriggerAutoscan, { wrapper });
  act(() => result.current.mutate());
  await waitFor(() => expect(release).toBeTypeOf("function"));
  act(() => setProfileId("profile-b"));
  await act(async () => release(response()));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(invalidate).not.toHaveBeenCalled();
  expect(result.current.data).toBeUndefined();
});
