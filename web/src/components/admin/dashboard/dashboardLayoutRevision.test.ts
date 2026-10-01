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
it("retains original read version through debounce and acknowledges retained newer edits despite competitor GET", async () => {
  let finish!: (r: Response) => void;
  let gets = 0;
  const writes: RequestInit[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((_url, init: RequestInit) => {
      if (init.method === "PUT") {
        writes.push(init);
        return writes.length === 1
          ? new Promise<Response>((r) => {
              finish = r;
            })
          : Promise.resolve(saved('"D"'));
      }
      return Promise.resolve(++gets === 1 ? read('"A"', 5) : read('"C"', 9));
    }),
  );
  const { result } = renderHook(useDashboardLayout, fixture());
  await settle();
  await settle();
  act(() => result.current.resizeWidget("users", { span: 6 }));
  await debounce();
  expect(writes).toHaveLength(1);
  expect(new Headers(writes[0]!.headers).get("If-Match")).toBe('"A"');
  act(() => result.current.resizeWidget("users", { span: 7 }));
  await debounce();
  expect(writes).toHaveLength(1);
  await act(async () => finish(saved('"B"')));
  await settle();
  await settle();
  expect(writes).toHaveLength(2);
  expect(new Headers(writes[1]!.headers).get("If-Match")).toBe('"B"');
  expect(JSON.parse(String(writes[1]!.body)).layout.entries[0].span).toBe(7);
  expect(result.current.entries[0]?.span).toBe(7);
});
it.each(["conflict", "lost"])(
  "stops queued edits after %s until explicit server reload",
  async (failure) => {
    let gets = 0;
    const writes: RequestInit[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((_url, init: RequestInit) => {
        if (init.method === "PUT") {
          writes.push(init);
          return failure === "lost"
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
              );
        }
        return Promise.resolve(++gets === 1 ? read('"A"', 5) : read('"C"', 9));
      }),
    );
    const { result } = renderHook(useDashboardLayout, fixture());
    await settle();
    await settle();
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
  const writes: RequestInit[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((_url, init: RequestInit) => {
      if (init.method === "PUT") {
        writes.push(init);
        return Promise.resolve(saved('"B"'));
      }
      return Promise.resolve(read('"A"', 5));
    }),
  );
  const { result, unmount } = renderHook(useDashboardLayout, fixture());
  await settle();
  await settle();
  act(() => result.current.resizeWidget("users", { span: 6 }));
  unmount();
  await settle();
  expect(writes).toHaveLength(1);
  expect(new Headers(writes[0]!.headers).get("If-Match")).toBe('"A"');
});

it("does not adopt an explicit reload over an edit made while it waits", async () => {
  let gets = 0;
  let finish!: (r: Response) => void;
  const writes: RequestInit[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((_url, init: RequestInit) => {
      if (init.method === "PUT") {
        writes.push(init);
        return Promise.resolve(saved('"B"'));
      }
      return ++gets === 1
        ? Promise.resolve(read('"A"', 5))
        : new Promise<Response>((r) => {
            finish = r;
          });
    }),
  );
  const { result } = renderHook(useDashboardLayout, fixture());
  await settle();
  await settle();
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
  expect(new Headers(writes[0]!.headers).get("If-Match")).toBe('"A"');
});
it("retains baseline authority across rerender and refuses a new PIN using an old draft", async () => {
  const writes: RequestInit[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((_url, init: RequestInit) => {
      if (init.method === "PUT") {
        writes.push(init);
        return Promise.resolve(saved('"B"'));
      }
      return Promise.resolve(read('"A"', 5));
    }),
  );
  const { result, rerender } = renderHook(useDashboardLayout, fixture());
  await settle();
  await settle();
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
  const writes: RequestInit[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((_url, init: RequestInit) => {
      if (init.method === "PUT") {
        writes.push(init);
        return writes.length === 1
          ? new Promise<Response>((r) => {
              finish = r;
            })
          : Promise.resolve(saved('"C"'));
      }
      return Promise.resolve(read('"A"', 5));
    }),
  );
  const { result, unmount } = renderHook(useDashboardLayout, fixture());
  await settle();
  await settle();
  act(() => result.current.resizeWidget("users", { span: 6 }));
  await debounce();
  act(() => result.current.resizeWidget("users", { span: 7 }));
  unmount();
  expect(writes).toHaveLength(1);
  await act(async () => finish(saved('"B"')));
  await settle();
  expect(writes).toHaveLength(2);
  expect(new Headers(writes[1]!.headers).get("If-Match")).toBe('"B"');
  expect(JSON.parse(String(writes[1]!.body)).layout.entries[0].span).toBe(7);
});
