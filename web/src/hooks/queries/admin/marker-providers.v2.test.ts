// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { installPolicyStorageMocks, jsonResponse } from "@/pages/admin-policy/policyTestUtils";
import { useMarkerProviders, useUpdateMarkerProvider, useValidateMarkerProvider } from "./markers";
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
beforeEach(installPolicyStorageMocks);
afterEach(() => vi.unstubAllGlobals());
function setup(body: unknown, status = 200) {
  const fetchMock = vi.fn<typeof fetch>(async () => jsonResponse(body, status));
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: 3 }, queries: { retry: false } },
  });
  return {
    fetchMock,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
it("does not refresh or replay provider update after401", async () => {
  const { fetchMock, wrapper } = setup({ message: "Expired" }, 401);
  const { result } = renderHook(useUpdateMarkerProvider, { wrapper });
  await act(async () => {
    await expect(
      result.current.mutateAsync({ provider: "plugin:7:markers", patch: { fetch_enabled: false } }),
    ).rejects.toThrow();
  });
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
    "/api/v2/admin/markers/providers/plugin%3A7%3Amarkers",
  );
});
it("does not refresh or replay validation after401", async () => {
  const { fetchMock, wrapper } = setup({ message: "Expired" }, 401);
  const { result } = renderHook(useValidateMarkerProvider, { wrapper });
  await act(async () => {
    await expect(result.current.mutateAsync({ provider: "plugin:7:markers" })).rejects.toThrow();
  });
  expect(fetchMock).toHaveBeenCalledTimes(1);
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe(
    "/api/v2/admin/markers/providers/plugin%3A7%3Amarkers/validate",
  );
});
it("preserves explicit false and zero updates", async () => {
  const { fetchMock, wrapper } = setup({ provider: "p", fetch_enabled: false, fetch_priority: 0 });
  const { result } = renderHook(useUpdateMarkerProvider, { wrapper });
  await act(async () => {
    await result.current.mutateAsync({
      provider: "p",
      patch: { fetch_enabled: false, fetch_priority: 0 },
    });
  });
  expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
    fetch_enabled: false,
    fetch_priority: 0,
  });
});
it("loads typed provider identity", async () => {
  const data = { providers: [{ provider: "p", plugin_installation_id: "9007199254740993" }] };
  const { fetchMock, wrapper } = setup(data);
  const { result } = renderHook(useMarkerProviders, { wrapper });
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toEqual(data);
  expect(String(fetchMock.mock.calls[0]?.[0])).toBe("/api/v2/admin/markers/providers");
});
