import { createElement, type ReactNode } from "react";
import { onlineManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { fetchPluginCatalogSettings, useUpdatePluginCatalogSettings } from "./plugins";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken("synthetic-refresh");
  setProfileId("primary-one");
  setProfileToken("synthetic-pin");
});
afterEach(() => {
  onlineManager.setOnline(true);
  setAccessToken(null);
  setRefreshToken(null);
  setProfileId(null);
  setProfileToken(null);
  vi.unstubAllGlobals();
});
function wrapper({ children }: { children: ReactNode }) {
  return createElement(
    QueryClientProvider,
    { client: new QueryClient({ defaultOptions: { mutations: { retry: 3, retryDelay: 0 } } }) },
    children,
  );
}
const response = (body: unknown, status = 200, etag?: string) =>
  new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": status >= 400 ? "application/problem+json" : "application/json",
      ...(etag ? { ETag: etag } : {}),
    },
  });

it("combines canonical configuration and counts without caching authority", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input) =>
      String(input).endsWith("catalog-settings")
        ? response({ include_approved_community_plugins: false }, 200, '"snapshot"')
        : response({
            approved_community_plugin_count: 2,
            installed_community_plugin_count: 1,
            migrated_plugin_count: 0,
            community_updates_paused: false,
          }),
    ),
  );
  const value = await fetchPluginCatalogSettings();
  expect(value.etag).toBe('"snapshot"');
  expect(value.community_updates_paused).toBe(true);
  expect(JSON.stringify(value)).not.toContain("synthetic");
});
it("sends the captured validator once and excludes local authority fields from the body", async () => {
  const fetchMock = vi.fn<typeof fetch>(async () =>
    response({ include_approved_community_plugins: true }, 200, '"next"'),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useUpdatePluginCatalogSettings(), { wrapper });
  act(() =>
    result.current.mutate({ include_approved_community_plugins: true, etag: '"snapshot"' }),
  );
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const init = fetchMock.mock.calls[0]?.[1];
  expect(new Headers(init?.headers).get("If-Match")).toBe('"snapshot"');
  expect(JSON.parse(String(init?.body))).toEqual({ include_approved_community_plugins: true });
});
it("refuses an offline edit after account authority changes", async () => {
  onlineManager.setOnline(false);
  const fetchMock = vi.fn<typeof fetch>();
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useUpdatePluginCatalogSettings(), { wrapper });
  act(() =>
    result.current.mutate({ include_approved_community_plugins: true, etag: '"snapshot"' }),
  );
  await waitFor(() => expect(result.current.isPaused).toBe(true));
  setAccessToken("different-account");
  act(() => onlineManager.setOnline(true));
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).not.toHaveBeenCalled();
});
it.each([401, 412])("does not refresh, rebase or replay after %s", async (status) => {
  const fetchMock = vi.fn<typeof fetch>(async () =>
    response({ type: "about:blank", title: "Rejected", status }, status),
  );
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useUpdatePluginCatalogSettings(), { wrapper });
  act(() =>
    result.current.mutate({ include_approved_community_plugins: true, etag: '"snapshot"' }),
  );
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(1);
});
