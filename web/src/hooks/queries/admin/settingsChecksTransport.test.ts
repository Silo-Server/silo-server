import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useCheckAdminSettingsConnection } from "./settings";

afterEach(() => {
  setAccessToken(null);
  setRefreshToken(null);
  setProfileId(null);
  setProfileToken(null);
  vi.unstubAllGlobals();
});

it("does not refresh or replay a connection-check POST after 401", async () => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("expired-synthetic-token");
  setRefreshToken("synthetic-refresh-token");
  const fetchMock = vi.fn<typeof fetch>(
    async () =>
      new Response(
        JSON.stringify({
          type: "https://silo.dev/problems/authentication_required",
          title: "Authentication required",
          status: 401,
          code: "authentication_required",
        }),
        { status: 401, headers: { "Content-Type": "application/problem+json" } },
      ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({ defaultOptions: { mutations: { retry: 3, retryDelay: 0 } } });
  function wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children);
  }
  const { result } = renderHook(() => useCheckAdminSettingsConnection(), { wrapper });
  await act(async () => {
    await expect(
      result.current.mutateAsync({ kind: "redis", body: { values: {}, dirty_keys: [] } }),
    ).rejects.toMatchObject({ status: 401 });
  });
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/admin/settings/check/redis");
  expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("POST");
});
