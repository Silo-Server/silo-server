import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useScanLibrary, useCancelLibraryScans } from "./libraries";
import { toast } from "sonner";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function fixture() {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: 3 } } });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  return { client, wrapper };
}
beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("test-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
  onlineManager.setOnline(true);
});
afterEach(() => {
  cleanup();
  onlineManager.setOnline(true);
  vi.unstubAllGlobals();
});

for (const [useCommand, path, body] of [
  [useScanLibrary, "/api/v2/scan", { status: "accepted", mode: "library", library_id: "42" }],
  [useCancelLibraryScans, "/api/v2/scan/cancel", { cancelled: 2, library_id: "42" }],
] as const) {
  const response = () =>
    new Response(JSON.stringify(body), {
      status: path.endsWith("cancel") ? 200 : 202,
      headers: { "Content-Type": "application/json" },
    });
  it(`${path} sends exact string ID once and preserves selected numeric UI identity`, async () => {
    const fetchMock = vi.fn().mockResolvedValue(response());
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useCommand(), fixture());
    act(() => result.current.mutate(42));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.variables).toBe(42);
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls[0]?.[0]).toBe(path);
    expect(JSON.parse(fetchMock.mock.calls[0]?.[1].body)).toEqual({ library_id: "42" });
    expect(toast.success).toHaveBeenCalledOnce();
  });
  it(`${path} retains offline submission authority and suppresses stale cache/toast effects`, async () => {
    onlineManager.setOnline(false);
    const fetchMock = vi.fn().mockResolvedValue(response());
    vi.stubGlobal("fetch", fetchMock);
    const { client, wrapper } = fixture();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const { result } = renderHook(() => useCommand(), { wrapper });
    act(() => result.current.mutate(42));
    await waitFor(() => expect(result.current.isPaused).toBe(true));
    act(() => {
      setProfileId("profile-b");
      setProfileToken("pin-b");
      onlineManager.setOnline(true);
    });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(fetchMock.mock.calls[0]?.[1].headers).toMatchObject({
      "X-Profile-Id": "profile-a",
      "X-Profile-Token": "pin-a",
    });
    expect(toast.success).not.toHaveBeenCalled();
    expect(invalidate).not.toHaveBeenCalled();
  });
  it(`${path} does not refresh or replay an uncertain command on 401`, async () => {
    setRefreshToken("refresh-token");
    const fetchMock = vi.fn().mockImplementation(
      async () =>
        new Response(
          JSON.stringify({
            type: "https://silo.example/problems/unauthenticated",
            title: "Unauthorized",
            status: 401,
          }),
          { status: 401, headers: { "Content-Type": "application/problem+json" } },
        ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useCommand(), fixture());
    act(() => result.current.mutate(42));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls[0]?.[0]).toBe(path);
  });
}
