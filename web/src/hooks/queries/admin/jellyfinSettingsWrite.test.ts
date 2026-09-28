import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider, onlineManager } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useAdminServerSettings, useUpdateJellyfinCompatSettings } from "./settings";
import { toast } from "sonner";
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));
function fixture() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  return { client, wrapper };
}
const response = (body: unknown, status = 200, etag = '"displayed-v1"') =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ETag: etag },
  });
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

it("sends the displayed settings guard and invalidates the exact compatibility scope", async () => {
  const fetchMock = vi
    .fn()
    .mockImplementation(async () => response({ "jellyfin_compat.web_enabled": "false" }));
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const invalidate = vi.spyOn(client, "invalidateQueries");
  const { result } = renderHook(
    () => ({ read: useAdminServerSettings(), write: useUpdateJellyfinCompatSettings() }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.read.isSuccess).toBe(true));
  await act(async () => {
    await result.current.write.mutateAsync({ web_enabled: true });
  });
  const patches = fetchMock.mock.calls.filter(([, init]) => init?.method === "PATCH");
  expect(patches).toHaveLength(1);
  expect(patches[0]?.[0]).toBe("/api/v2/admin/jellyfin-compat/settings");
  expect(patches[0]?.[1].headers["If-Match"]).toBe('"displayed-v1"');
  expect(
    invalidate.mock.calls.some(
      ([options]) => options?.exact === true && options.queryKey?.includes("profile-a"),
    ),
  ).toBe(true);
});
it("captures copied toggle intent and authority before offline pause without stale effects", async () => {
  const fetchMock = vi
    .fn()
    .mockImplementation(async () => response({ "jellyfin_compat.web_enabled": "false" }));
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const invalidate = vi.spyOn(client, "invalidateQueries");
  const { result } = renderHook(
    () => ({ read: useAdminServerSettings(), write: useUpdateJellyfinCompatSettings() }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.read.isSuccess).toBe(true));
  onlineManager.setOnline(false);
  const body = { web_enabled: true };
  act(() => result.current.write.mutate(body));
  body.web_enabled = false;
  await waitFor(() => expect(result.current.write.isPaused).toBe(true));
  act(() => {
    setProfileId("profile-b");
    setProfileToken("pin-b");
    onlineManager.setOnline(true);
  });
  await waitFor(() => expect(result.current.write.isSuccess).toBe(true));
  const patches = fetchMock.mock.calls.filter(([, init]) => init?.method === "PATCH");
  expect(patches).toHaveLength(1);
  expect(patches[0]?.[1].headers).toMatchObject({
    "X-Profile-Id": "profile-a",
    "X-Profile-Token": "pin-a",
    "If-Match": '"displayed-v1"',
  });
  expect(JSON.parse(patches[0]?.[1].body)).toEqual({ web_enabled: true });
  expect(invalidate).not.toHaveBeenCalled();
  expect(toast.error).not.toHaveBeenCalled();
});
it("does not refresh or replay a PATCH rejected with401", async () => {
  setRefreshToken("refresh-token");
  const fetchMock = vi.fn().mockImplementation(async (_url, init) =>
    init?.method === "PATCH"
      ? response(
          {
            type: "https://siloserver.org/docs/api/v2/problems/invalid_token",
            title: "Unauthorized",
            status: 401,
          },
          401,
        )
      : response({ "jellyfin_compat.web_enabled": "false" }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () => ({ read: useAdminServerSettings(), write: useUpdateJellyfinCompatSettings() }),
    fixture(),
  );
  await waitFor(() => expect(result.current.read.isSuccess).toBe(true));
  await act(async () => {
    await expect(result.current.write.mutateAsync({ web_enabled: true })).rejects.toThrow();
  });
  expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "PATCH")).toHaveLength(1);
  expect(fetchMock.mock.calls.some(([url]) => String(url).includes("refresh"))).toBe(false);
});
