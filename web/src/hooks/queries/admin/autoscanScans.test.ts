import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useAutoscanScans } from "../useAutoscan";
const row = {
  id: "scan-a",
  library_id: "7",
  mode: "file",
  trigger: "autoscan",
  status: "completed",
  event_status: "success",
  autoscan_event_id: "9",
};
function response(items: unknown[] = [row], next = "", total = 1) {
  return new Response(
    JSON.stringify({ items, total, page: { has_more: !!next, next_cursor: next } }),
    { headers: { "Content-Type": "application/json" } },
  );
}
function fixture() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    client,
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic");
  setProfileId("profile-a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("follows signed pages to the requested numbered page and adapts IDs", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValueOnce(response([row], "next", 2))
    .mockResolvedValueOnce(response([{ ...row, id: "scan-b" }], "", 2));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(() => useAutoscanScans({ limit: 1, offset: 1 }), fixture());
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data).toEqual({
    rows: [{ ...row, id: "scan-b", library_id: 7, autoscan_event_id: 9 }],
    total: 2,
  });
  expect(fetchMock).toHaveBeenCalledTimes(2);
  expect(String(fetchMock.mock.calls[1]?.[0])).toContain("cursor=next");
});
it.each([null, "pin-b"])("isolates cached success on PIN transition %s", async (pin) => {
  const fetchMock = vi.fn().mockResolvedValue(response());
  vi.stubGlobal("fetch", fetchMock);
  const { result, rerender } = renderHook(() => useAutoscanScans(), fixture());
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  fetchMock.mockImplementation(() => new Promise(() => {}));
  act(() => setProfileToken(pin));
  rerender();
  expect(result.current.data).toBeUndefined();
  await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
});
it.each(["unsafe", "loop"])("rejects %s data without partial success", async (failure) => {
  const fetchMock =
    failure === "unsafe"
      ? vi.fn().mockResolvedValue(response([{ ...row, autoscan_event_id: "9007199254740993" }]))
      : vi.fn().mockResolvedValue(response([row], "same", 3));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(
    () => useAutoscanScans({ limit: 1, offset: failure === "loop" ? 2 : 0 }),
    fixture(),
  );
  await waitFor(() => expect(result.current.isError).toBe(true));
  expect(result.current.data).toBeUndefined();
});
it("rejects late decoded rows after authority replacement", async () => {
  let release!: (r: Response) => void;
  vi.stubGlobal(
    "fetch",
    vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          release = resolve;
        }),
    ),
  );
  const f = fixture();
  const { result } = renderHook(() => useAutoscanScans(), f);
  const original = f.client.getQueryCache().getAll()[0]!;
  await waitFor(() => expect(release).toBeTypeOf("function"));
  act(() => setProfileId("profile-b"));
  await act(async () => release(response()));
  await waitFor(() => expect(original.state.status).toBe("error"));
  expect(original.state.data).toBeUndefined();
  expect(result.current.data).toBeUndefined();
});
