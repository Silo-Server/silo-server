import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { fetchPluginCatalog, useAdminPluginInstallations, useAdminPlugins } from "./plugins";

function fixture() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
const manifestSurface = {
  capabilities: [],
  global_config_schema: [],
  user_config_schema: [],
  routes: [
    {
      id: "admin",
      method: "GET",
      path: "/admin",
      access: "admin",
      navigable: true,
      navigation_label: "Synthetic",
      navigation_kind: "admin",
      static_asset: false,
    },
  ],
  assets: [],
  metadata: {},
};
function installation(id: string, next?: string) {
  return {
    items: [
      {
        id,
        repository_id: "4",
        plugin_id: `org.example.${id}`,
        version: "1.0.0",
        install_path: `/plugins/${id}`,
        enabled: true,
        kind: "plugin",
        update_policy: "manual",
        source_kind: "silo",
        updates_paused: false,
        ...manifestSurface,
        global_configs: [
          { key: "account", value: { region: "us-east" }, configured_secrets: ["api_key"] },
        ],
        auth_bindings: [],
        task_bindings: [],
        created_at: "2026-09-01T00:00:00.123Z",
        updated_at: "2026-09-01T00:00:00.123Z",
      },
    ],
    page: { has_more: !!next, next_cursor: next },
  };
}
function catalogEntry(pluginId: string, next?: string) {
  return {
    items: [
      {
        repository_id: "2",
        plugin_id: pluginId,
        version: "1.0.0",
        archive_url: "https://example.invalid/a.zip",
        source_kind: "external",
        repository_name: "Synthetic",
        ...manifestSurface,
      },
    ],
    page: { has_more: !!next, next_cursor: next },
  };
}
const response = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic-admin");
  setRefreshToken(null);
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("drains installation pages, adapts string IDs and keeps redacted configuration", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response(installation("3", "next")))
    .mockResolvedValueOnce(response(installation("9")));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useAdminPluginInstallations, fixture());
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data?.map((r) => r.id)).toEqual([3, 9]);
  expect(result.current.data?.[0]?.repository_id).toBe(4);
  expect(result.current.data?.[0]?.available_version).toBeNull();
  expect(result.current.data?.[0]?.global_configs[0]?.configured_secrets).toEqual(["api_key"]);
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/v2/admin/plugins/installations");
  expect(String(fetchMock.mock.calls[1]?.[0])).toContain("cursor=next");
  const init = fetchMock.mock.calls[0]?.[1] as RequestInit;
  const headers = init.headers as Record<string, string>;
  expect(headers["X-Profile-Id"]).toBe("profile-a");
});
it("fails a drain on a repeated continuation instead of merging pages", async () => {
  const fetchMock = vi.fn().mockResolvedValue(response(installation("3", "loop")));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useAdminPluginInstallations, fixture());
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(fetchMock).toHaveBeenCalledTimes(2);
});
it("refuses a duplicated installation identifier", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(installation("3"))));
  const first = installation("3", "next");
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response(first))
    .mockResolvedValueOnce(response(installation("3")));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useAdminPluginInstallations, fixture());
  await waitFor(() => expect(result.current.isError).toBe(true));
});
it.each([null, "pin-b", "pin-a"])(
  "hides cached installation success on PIN setter %s",
  async (pin) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(installation("3")))
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { client, wrapper } = fixture();
    const { result, rerender } = renderHook(useAdminPluginInstallations, { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    act(() => setProfileToken(pin));
    rerender();
    expect(result.current.data).toBeUndefined();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((q) => q.queryKey),
      ),
    ).not.toMatch(/pin-a|pin-b/);
  },
);
it("reads the catalog through v2 and rejects an unknown source kind", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response(catalogEntry("org.example.a", "next")))
    .mockResolvedValueOnce(response(catalogEntry("org.example.b")));
  vi.stubGlobal("fetch", fetchMock);
  const rows = await fetchPluginCatalog();
  expect(rows.map((r) => r.plugin_id)).toEqual(["org.example.a", "org.example.b"]);
  expect(rows[0]?.repository_id).toBe(2);
  expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/api/v2/admin/plugins/catalog?limit=100");
  const bad = catalogEntry("org.example.c");
  bad.items[0]!.source_kind = "mystery";
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(bad)));
  await expect(fetchPluginCatalog()).rejects.toThrow("Unrecognized plugin source kind.");
});
it("composes the plugins page reads without a legacy call", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/admin/plugins/catalog-settings"))
      return Promise.resolve(
        new Response(JSON.stringify({ include_approved_community_plugins: true }), {
          headers: { "Content-Type": "application/json", ETag: '"1"' },
        }),
      );
    if (url.includes("/admin/plugins/catalog-status"))
      return Promise.resolve(
        response({
          approved_community_plugin_count: 0,
          installed_community_plugin_count: 0,
          migrated_plugin_count: 0,
          community_updates_paused: false,
        }),
      );
    if (url.includes("/admin/plugins/catalog"))
      return Promise.resolve(response(catalogEntry("org.example.a")));
    if (url.includes("/admin/plugins/installations"))
      return Promise.resolve(response(installation("3")));
    if (url.includes("/admin/plugins/repositories"))
      return Promise.resolve(response({ items: [], page: { has_more: false } }));
    return Promise.reject(new Error(`unexpected ${url}`));
  });
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useAdminPlugins, fixture());
  await waitFor(() => expect(result.current.isLoading).toBe(false));
  expect(result.current.catalog.map((c) => c.plugin_id)).toEqual(["org.example.a"]);
  expect(result.current.installations.map((i) => i.id)).toEqual([3]);
  expect(fetchMock.mock.calls.every((c) => String(c[0]).includes("/api/v2/"))).toBe(true);
});
