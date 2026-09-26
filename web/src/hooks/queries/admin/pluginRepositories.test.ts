import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useAdminPluginRepositories } from "./plugins";
function fixture() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
function page(id = "2", next?: string) {
  return {
    items: [
      {
        id,
        url: "https://example.invalid/index.json",
        display_name: "Synthetic",
        enabled: true,
        source_kind: "external",
        managed: false,
        created_at: "2026-09-01T00:00:00.123Z",
        updated_at: "2026-09-01T00:00:00.123Z",
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
it("drains canonical pages and adapts string IDs", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response(page("2", "next")))
    .mockResolvedValueOnce(response(page("9")));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useAdminPluginRepositories, fixture());
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data?.map((r) => r.id)).toEqual([2, 9]);
  expect(String(fetchMock.mock.calls[1]?.[0])).toContain("cursor=next");
});
it.each([null, "pin-b", "pin-a"])(
  "hides cached repository success on PIN setter %s",
  async (pin) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(page()))
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { client, wrapper } = fixture();
    const { result, rerender } = renderHook(useAdminPluginRepositories, { wrapper });
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
it.each(["duplicate", "unsafe", "loop"])(
  "refuses %s without publishing partial repositories",
  async (kind) => {
    const second =
      kind === "unsafe"
        ? page("9007199254740993")
        : kind === "duplicate"
          ? page("2")
          : page("9", "next");
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(response(page("2", "next")))
        .mockResolvedValueOnce(response(second)),
    );
    const { result } = renderHook(useAdminPluginRepositories, fixture());
    await waitFor(() => expect(result.current.isError).toBe(true));
    expect(result.current.data).toBeUndefined();
  },
);
it("rejects a decoded page after authority changes", async () => {
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
  renderHook(useAdminPluginRepositories, { wrapper });
  await waitFor(() => expect(release).toBeTypeOf("function"));
  const key = client.getQueryCache().getAll()[0]!.queryKey;
  act(() => setProfileId("profile-b"));
  await act(async () => release(JSON.stringify(page())));
  await waitFor(() => expect(client.getQueryState(key)?.status).toBe("error"));
  expect(client.getQueryData(key)).toBeUndefined();
});
it("legacy repository prefix invalidation refreshes the scoped reader", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response(page("2")))
    .mockResolvedValueOnce(response(page("9")));
  vi.stubGlobal("fetch", fetchMock);
  const { client, wrapper } = fixture();
  const { result } = renderHook(useAdminPluginRepositories, { wrapper });
  await waitFor(() => expect(result.current.data?.[0]?.id).toBe(2));
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["admin", "plugins", "repositories"] });
  });
  await waitFor(() => expect(result.current.data?.[0]?.id).toBe(9));
});
