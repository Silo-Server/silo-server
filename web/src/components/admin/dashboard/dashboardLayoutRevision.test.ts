import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import { useDashboardLayout, DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS } from "./useDashboardLayout";
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { id: 1 } }) }));
const document = (span: number) => ({ version: 1, entries: [{ id: "users", span, rows: 4 }] });
const read = (tag: string, span: number) =>
  new Response(JSON.stringify({ layout: document(span), updated_at: null }), {
    headers: { "Content-Type": "application/json", ETag: tag },
  });
const saved = (tag: string) => new Response(null, { status: 204, headers: { ETag: tag } });
function fixture() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  return {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
async function settle() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
}
async function debounce() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS + 1);
  });
}
beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic");
  setProfileId("a");
  setProfileToken("pin-a");
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
/** Stubs fetch with `onPut` for layout writes and `onGet` for reads; returns the writes it saw. */
function stubFetch(
  onPut: (write: number) => Promise<Response>,
  onGet: (read: number) => Promise<Response> = () => Promise.resolve(read('"A"', 5)),
) {
  const writes: RequestInit[] = [];
  let gets = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn((_url, init: RequestInit) => {
      if (init.method === "PUT") {
        writes.push(init);
        return onPut(writes.length);
      }
      return onGet(++gets);
    }),
  );
  return writes;
}
async function mountLoaded() {
  const view = renderHook(useDashboardLayout, fixture());
  await settle();
  await settle();
  return view;
}
const ifMatch = (write: RequestInit | undefined) => new Headers(write!.headers).get("If-Match");
const firstThenSecond = (n: number) => Promise.resolve(n === 1 ? read('"A"', 5) : read('"C"', 9));
it("retains original read version through debounce and acknowledges retained newer edits despite competitor GET", async () => {
  let finish!: (r: Response) => void;
  const writes = stubFetch(
    (n) =>
      n === 1
        ? new Promise<Response>((r) => {
            finish = r;
          })
        : Promise.resolve(saved('"D"')),
    firstThenSecond,
  );
  const { result } = await mountLoaded();
  act(() => result.current.resizeWidget("users", { span: 6 }));
  await debounce();
  expect(writes).toHaveLength(1);
  expect(ifMatch(writes[0])).toBe('"A"');
  act(() => result.current.resizeWidget("users", { span: 7 }));
  await debounce();
  expect(writes).toHaveLength(1);
  await act(async () => finish(saved('"B"')));
  await settle();
  await settle();
  expect(writes).toHaveLength(2);
  expect(ifMatch(writes[1])).toBe('"B"');
  expect(JSON.parse(String(writes[1]!.body)).layout.entries[0].span).toBe(7);
  expect(result.current.entries[0]?.span).toBe(7);
});
it.each(["conflict", "lost"])(
  "stops queued edits after %s until explicit server reload",
  async (failure) => {
    const writes = stubFetch(
      () =>
        failure === "lost"
          ? Promise.reject(new Error("lost response"))
          : Promise.resolve(
              new Response(
                JSON.stringify({
                  type: "https://silo.dev/problems/stale_version",
                  title: "Stale",
                  status: 412,
                  detail: "Changed",
                  instance: "synthetic",
                }),
                { status: 412, headers: { "Content-Type": "application/problem+json" } },
              ),
            ),
      firstThenSecond,
    );
    const { result } = await mountLoaded();
    act(() => result.current.resizeWidget("users", { span: 6 }));
    await debounce();
    await settle();
    expect(result.current.serverSaveBlocked).toBe(true);
    act(() => result.current.resizeWidget("users", { span: 7 }));
    await debounce();
    expect(writes).toHaveLength(1);
    expect(result.current.entries[0]?.span).toBe(7);
    await act(async () => result.current.reloadServerLayout());
    await settle();
    expect(result.current.entries[0]?.span).toBe(8);
    expect(result.current.serverSaveBlocked).toBe(false);
    expect(writes).toHaveLength(1);
  },
);
it("cleanup flush preserves the original read validator", async () => {
  const writes = stubFetch(() => Promise.resolve(saved('"B"')));
  const { result, unmount } = await mountLoaded();
  act(() => result.current.resizeWidget("users", { span: 6, rows: 5 }));
  expect(writes).toHaveLength(0);
  unmount();
  await settle();
  expect(writes).toHaveLength(1);
  expect(JSON.parse(String(writes[0]!.body)).layout).toEqual({
    version: 1,
    entries: [{ id: "users", span: 6, rows: 5 }],
  });
  expect(ifMatch(writes[0])).toBe('"A"');
});

it("does not adopt an explicit reload over an edit made while it waits", async () => {
  let finish!: (r: Response) => void;
  const writes = stubFetch(
    () => Promise.resolve(saved('"B"')),
    (n) =>
      n === 1
        ? Promise.resolve(read('"A"', 5))
        : new Promise<Response>((r) => {
            finish = r;
          }),
  );
  const { result } = await mountLoaded();
  let reload!: Promise<void>;
  act(() => {
    reload = result.current.reloadServerLayout();
  });
  await settle();
  act(() => result.current.resizeWidget("users", { span: 7 }));
  await act(async () => {
    finish(read('"C"', 8));
    await reload;
  });
  expect(result.current.entries[0]?.span).toBe(7);
  await debounce();
  expect(ifMatch(writes[0])).toBe('"A"');
});
it("retains baseline authority across rerender and refuses a new PIN using an old draft", async () => {
  const writes = stubFetch(() => Promise.resolve(saved('"B"')));
  const { result, rerender } = await mountLoaded();
  act(() => {
    setProfileToken("pin-b");
    rerender();
  });
  await settle();
  act(() => result.current.resizeWidget("users", { span: 7 }));
  await debounce();
  expect(writes).toHaveLength(0);
  expect(result.current.entries[0]?.span).toBe(7);
  expect(result.current.serverSaveBlocked).toBe(true);
});

it("unmount retains queued edit until the in-flight write acknowledges its version", async () => {
  let finish!: (r: Response) => void;
  const writes = stubFetch((n) =>
    n === 1
      ? new Promise<Response>((r) => {
          finish = r;
        })
      : Promise.resolve(saved('"C"')),
  );
  const { result, unmount } = await mountLoaded();
  act(() => result.current.resizeWidget("users", { span: 6 }));
  await debounce();
  act(() => result.current.resizeWidget("users", { span: 7 }));
  unmount();
  expect(writes).toHaveLength(1);
  await act(async () => finish(saved('"B"')));
  await settle();
  expect(writes).toHaveLength(2);
  expect(ifMatch(writes[1])).toBe('"B"');
  expect(JSON.parse(String(writes[1]!.body)).layout.entries[0].span).toBe(7);
});
