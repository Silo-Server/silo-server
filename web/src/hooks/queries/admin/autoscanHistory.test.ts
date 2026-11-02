import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, it, expect, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useAutoscanEvents, useAutoscanScans } from "../useAutoscan";
const event = {
  id: "7",
  source_id: null,
  plugin_id: "synthetic",
  capability_id: "scan",
  started_at: "2026-09-01T00:00:00.000Z",
  completed_at: "2026-09-01T00:00:01.000Z",
  duration_ms: 1000,
  status: "success",
  delivery_mode: "poll",
  changes_returned: 1,
  changes_resolved: 1,
  targets_claimed: 1,
  scans_created: 1,
  scans_reused: 0,
  scans_suppressed: 0,
  scan_runs: [
    { id: "run-a", library_id: "9", mode: "file", trigger: "autoscan", status: "completed" },
  ],
  changes: [
    {
      source_path: "/data/tv/a.mkv",
      rewritten_path: "/mnt/tv/a.mkv",
      outcome: "joined",
      library_id: "9",
      scan_run_id: "run-a",
    },
  ],
  changes_truncated: false,
};
const scan = {
  id: "scan-a",
  library_id: "7",
  mode: "file",
  trigger: "autoscan",
  status: "completed",
  event_status: "success",
  autoscan_event_id: "9",
};
// Each reader: the wire row, the second page's row, how the hook adapts it,
// and a row carrying an ID the UI cannot represent.
const readers = [
  {
    name: "events",
    useRows: useAutoscanEvents,
    row: event,
    next: { ...event, id: "8" },
    adapted: {
      ...event,
      id: 8,
      scan_runs: [{ ...event.scan_runs[0], library_id: 9 }],
      changes: [{ ...event.changes[0], library_id: 9 }],
    },
    unsafe: { ...event, id: "9007199254740993" },
  },
  {
    name: "scans",
    useRows: useAutoscanScans,
    row: scan,
    next: { ...scan, id: "scan-b" },
    adapted: { ...scan, id: "scan-b", library_id: 7, autoscan_event_id: 9 },
    unsafe: { ...scan, autoscan_event_id: "9007199254740993" },
  },
];
function response(items: unknown[], next = "", total = 1) {
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
describe.each(readers)("autoscan $name", ({ useRows, row, next, adapted, unsafe }) => {
  it("follows signed pages to the requested numbered page and adapts IDs", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([row], "next", 2))
      .mockResolvedValueOnce(response([next], "", 2));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useRows({ limit: 1, offset: 1 }), fixture());
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toEqual({ rows: [adapted], total: 2 });
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain("cursor=next");
  });
  it.each([null, "pin-b"])("isolates cached success on PIN transition %s", async (pin) => {
    const fetchMock = vi.fn().mockResolvedValue(response([row]));
    vi.stubGlobal("fetch", fetchMock);
    const { result, rerender } = renderHook(() => useRows(), fixture());
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
        ? vi.fn().mockResolvedValue(response([unsafe]))
        : vi.fn().mockResolvedValue(response([row], "same", 3));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(
      () => useRows({ limit: 1, offset: failure === "loop" ? 2 : 0 }),
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
    const { result } = renderHook(() => useRows(), f);
    const original = f.client.getQueryCache().getAll()[0]!;
    await waitFor(() => expect(release).toBeTypeOf("function"));
    act(() => setProfileId("profile-b"));
    await act(async () => release(response([row])));
    await waitFor(() => expect(original.state.status).toBe("error"));
    expect(original.state.data).toBeUndefined();
    expect(result.current.data).toBeUndefined();
  });
});
it("passes an unknown change outcome through instead of failing the page", async () => {
  vi.stubGlobal(
    "fetch",
    vi
      .fn()
      .mockResolvedValue(
        response([{ ...event, changes: [{ ...event.changes[0], outcome: "teleported" }] }]),
      ),
  );
  const { result } = renderHook(() => useAutoscanEvents(), fixture());
  await waitFor(() => expect(result.current.isSuccess).toBe(true));
  expect(result.current.data?.rows[0]?.changes[0]?.outcome).toBe("teleported");
});
