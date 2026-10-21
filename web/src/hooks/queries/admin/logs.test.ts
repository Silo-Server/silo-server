import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useAuditLogs, useOperationalLogs } from "./logs";
function fixture() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
const readers = [
  {
    name: "audit",
    useLogs: useAuditLogs,
    item: {
      method: "GET",
      path: "/synthetic",
      client_ip: "192.0.2.7",
      status_code: 200,
      duration_ms: 12,
      impersonator_user_id: "9",
    },
  },
  {
    name: "operational",
    useLogs: useOperationalLogs,
    item: {
      level: "warn",
      component: "ffmpeg",
      message: "Synthetic warning",
      attrs: { attempt: 2 },
    },
  },
];
function page(item: object, id = "42") {
  return {
    items: [{ id, timestamp: "2026-09-06T00:00:00.123Z", ...item, user_id: "7" }],
    page: { has_more: true, next_cursor: "signed-next" },
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
describe.each(readers)("$name logs", ({ useLogs, item }) => {
  it("rejects a read decoded after the acting profile changes", async () => {
    let release: ((value: string) => void) | undefined;
    const pending = new Promise<string>((resolve) => {
      release = resolve;
    });
    const res = response({});
    res.text = () => pending;
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(res)
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { client, wrapper } = fixture();
    const { result } = renderHook(() => useLogs({ limit: 12 }), { wrapper });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    const originalKey = client.getQueryCache().getAll()[0]!.queryKey;
    act(() => setProfileId("profile-b"));
    await act(async () => {
      release?.(JSON.stringify(page(item)));
    });
    // The observer can move to the new profile's key when it rerenders. The
    // deferred old-profile response must fail its own cache entry, regardless.
    await waitFor(() => expect(client.getQueryState(originalKey)?.status).toBe("error"));
    expect(client.getQueryData(originalKey)).toBeUndefined();
    expect(result.current.data).toBeUndefined();
  });

  it.each(["pin-a"])("hides cached success when same-profile PIN changes to %s", async (proof) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(page(item)))
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { client, wrapper } = fixture();
    const { result, rerender } = renderHook(() => useLogs({}), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    act(() => setProfileToken(proof));
    rerender();
    expect(result.current.data).toBeUndefined();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((query) => query.queryKey),
      ),
    ).not.toContain("pin-a");
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((query) => query.queryKey),
      ),
    ).not.toContain("replacement-proof");
  });
});
it("rejects identifiers the current UI cannot represent rather than rounding", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(response(page(readers[0]!.item, "9007199254740993"))),
  );
  const { result } = renderHook(() => useAuditLogs({}), fixture());
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.error?.message).toContain("cannot be represented");
});
