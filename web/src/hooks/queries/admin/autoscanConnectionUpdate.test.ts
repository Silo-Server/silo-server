import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useUpdateAutoscanConnection } from "../useAutoscan";
const body = {
  name: "Synthetic",
  kind: "sonarr",
  base_url: "https://example.invalid",
  api_key_ref: "synthetic-ref",
};
const response = () =>
  new Response(
    JSON.stringify({ id: "created", name: "Synthetic", kind: "sonarr", has_api_key: true }),
    {
      status: 200,
      headers: { "Content-Type": "application/json" },
    },
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
it("copies the connection draft before offline queueing and sends once", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn().mockResolvedValue(response());
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useUpdateAutoscanConnection, fixture());
  const draft = { id: "connection-a", body: { ...body } };
  act(() => result.current.mutate(draft));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  draft.id = "connection-b";
  draft.body.base_url = "https://later.invalid";
  act(() => onlineManager.setOnline(true));
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledOnce();
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain(
    "/api/v2/admin/autoscan/connections/connection-a",
  );
  const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
  expect(init.method).toBe("PUT");
  expect(JSON.parse(String(init.body))).toEqual(body);
});
it("refuses a queued update after authority replacement", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useUpdateAutoscanConnection, fixture());
  act(() => result.current.mutate({ id: "connection-a", body }));
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  act(() => {
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});
it.each(["401", "network"])(
  "never replays uncertain %s connection update under global retry3",
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
    const { result } = renderHook(useUpdateAutoscanConnection, fixture());
    act(() => result.current.mutate({ id: "connection-a", body }));
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(fetchMock).toHaveBeenCalledOnce();
  },
);
it("does not invalidate the new authority after late connection update acknowledgement", async () => {
  let release!: (s: string) => void;
  const res = response();
  res.text = () =>
    new Promise((resolve) => {
      release = resolve;
    });
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(res));
  const { client, wrapper } = fixture();
  const invalidation = vi.spyOn(client, "invalidateQueries");
  const { result } = renderHook(useUpdateAutoscanConnection, { wrapper });
  act(() => result.current.mutate({ id: "connection-a", body }));
  await waitFor(() => expect(release).toBeTypeOf("function"));
  act(() => setProfileId("profile-b"));
  await act(async () =>
    release(
      JSON.stringify({ id: "created", name: "Synthetic", kind: "sonarr", has_api_key: true }),
    ),
  );
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(invalidation).not.toHaveBeenCalled();
});
