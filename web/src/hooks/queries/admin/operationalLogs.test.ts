import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setRefreshToken, setProfileId, setProfileToken } from "@/api/client";
import { useOperationalLogs } from "./logs";
function fixture() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
function page(id = "42") {
  return {
    items: [
      {
        id,
        timestamp: "2026-09-06T00:00:00.123Z",
        level: "warn",
        component: "ffmpeg",
        message: "Synthetic warning",
        user_id: "7",
        attrs: { attempt: 2 },
      },
    ],
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
it("adopts filters, full scoped continuation and checked UI identifiers", async () => {
  const fetchMock = vi.fn().mockResolvedValue(response(page()));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () =>
      useOperationalLogs({
        level: "error,warn",
        limit: 12,
        playback_session_id: "playback-42",
        cursor: "signed-prior",
      }),
    fixture(),
  );
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  const url = new URL(String(fetchMock.mock.calls[0]?.[0]), "https://example.invalid");
  expect(url.pathname).toBe("/api/v2/admin/logs/app");
  expect(url.searchParams.get("level")).toBe("error,warn");
  expect(url.searchParams.get("cursor")).toBe("signed-prior");
  expect(url.searchParams.get("playback_session_id")).toBe("playback-42");
  expect(result.current.data).toMatchObject({
    entries: [{ id: 42, user_id: 7, attrs: { attempt: 2 } }],
    next_cursor: "signed-next",
  });
});
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
  const { result } = renderHook(() => useOperationalLogs({ limit: 12 }), { wrapper });
  await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
  const originalKey = client.getQueryCache().getAll()[0]!.queryKey;
  act(() => setProfileId("profile-b"));
  await act(async () => {
    release?.(JSON.stringify(page()));
  });
  // The observer can move to the new profile's key when it rerenders. The
  // deferred old-profile response must fail its own cache entry, regardless.
  await waitFor(() => expect(client.getQueryState(originalKey)?.status).toBe("error"));
  expect(client.getQueryData(originalKey)).toBeUndefined();
  expect(result.current.data).toBeUndefined();
});
it("rejects identifiers the current UI cannot represent rather than rounding", async () => {
  vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response(page("9007199254740993"))));
  const { result } = renderHook(() => useOperationalLogs({}), fixture());
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.error?.message).toContain("cannot be represented");
});
it("does not read without an acting profile", () => {
  setProfileId(null);
  const fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  renderHook(() => useOperationalLogs({}), fixture());
  expect(fetchMock).not.toHaveBeenCalled();
});

it.each([null, "replacement-proof", "pin-a"])(
  "hides cached success when same-profile PIN changes to %s",
  async (proof) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response(page()))
      .mockImplementation(() => new Promise(() => {}));
    vi.stubGlobal("fetch", fetchMock);
    const { client, wrapper } = fixture();
    const { result, rerender } = renderHook(() => useOperationalLogs({}), { wrapper });
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
  },
);
