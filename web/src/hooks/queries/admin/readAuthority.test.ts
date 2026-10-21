import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useAdminPluginRepositories } from "./plugins";
import { useHWAccelDetection } from "./system";
function fixture() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
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
// Each admin reader carries its own authority checks, so each keeps a row.
describe.each([
  {
    name: "hardware inventory",
    useRead: useHWAccelDetection,
    pins: ["pin-a"],
    page: {
      resolved: "qsv",
      source: "local",
      intel_detected: true,
      render_devices: [],
      render_device_details: [],
      detected_backends: [{ backend: "qsv", verified: false, skipped: true }],
    },
  },
  {
    name: "plugin repositories",
    useRead: useAdminPluginRepositories,
    pins: [null, "pin-b", "pin-a"],
    page: {
      items: [
        {
          id: "2",
          url: "https://example.invalid/index.json",
          display_name: "Synthetic",
          enabled: true,
          source_kind: "external",
          managed: false,
          created_at: "2026-09-01T00:00:00.123Z",
          updated_at: "2026-09-01T00:00:00.123Z",
        },
      ],
      page: { has_more: false },
    },
  },
])("$name", ({ useRead, pins, page }) => {
  it.each(pins)("hides cached success after PIN setter %s", async (pin) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(page))
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { client, wrapper } = fixture();
    const { result, rerender } = renderHook(() => useRead(), { wrapper });
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
  });
  it("rejects a page decoded after profile replacement", async () => {
    let release!: (s: string) => void;
    const res = response({});
    res.text = () =>
      new Promise((resolve) => {
        release = resolve;
      });
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(res)
        .mockImplementation(() => new Promise(() => {})),
    );
    const { client, wrapper } = fixture();
    renderHook(() => useRead(), { wrapper });
    await waitFor(() => expect(release).toBeTypeOf("function"));
    const key = client.getQueryCache().getAll()[0]!.queryKey;
    act(() => setProfileId("profile-b"));
    await act(async () => release(JSON.stringify(page)));
    await waitFor(() => expect(client.getQueryState(key)?.status).toBe("error"));
    expect(client.getQueryData(key)).toBeUndefined();
  });
});
