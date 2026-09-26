// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useThemeCatalog, useRefreshThemeCatalog } from "./theme";
import { setAccessToken, setRefreshToken } from "@/api/client";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";

beforeEach(() => {
  installPolicyStorageMocks();
  setAccessToken("access");
  setRefreshToken("refresh");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
function wrapper({ children }: { children: ReactNode }) {
  return (
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } } })
      }
    >
      {children}
    </QueryClientProvider>
  );
}
it("reads the portable catalog envelope and publishes refreshed entries into the same query", async () => {
  const fetchMock = vi.fn<typeof fetch>(async (input) =>
    jsonResponse({
      document: {
        version: 1,
        themes: [{ id: String(input).endsWith("/refresh") ? "new" : "old", name: "Fixture" }],
      },
      stale: false,
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () => ({ catalog: useThemeCatalog(), refresh: useRefreshThemeCatalog() }),
    { wrapper },
  );
  await waitFor(() => expect(result.current.catalog.data?.[0]?.id).toBe("old"));
  await act(async () => {
    await result.current.refresh.mutateAsync();
  });
  await waitFor(() => expect(result.current.catalog.data?.[0]?.id).toBe("new"));
  expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
    "/api/v2/theme/catalog",
    "/api/v2/theme/catalog/refresh",
  ]);
  expect(fetchMock.mock.calls[1]?.[1]?.method).toBe("POST");
});
it("does not replay refresh on 401 through HTTP or globally configured mutation retries", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () =>
    jsonResponse(
      {
        type: "https://siloserver.org/docs/api/v2/problems/authentication_required",
        status: 401,
        title: "Authentication required",
      },
      401,
    ),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useRefreshThemeCatalog(), { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync()).rejects.toThrow();
  });
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
