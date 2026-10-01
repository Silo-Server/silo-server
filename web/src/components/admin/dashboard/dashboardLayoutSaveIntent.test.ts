import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, it, expect, vi } from "vitest";
import { setAccessToken, setProfileId, setProfileToken } from "@/api/client";
import {
  useDashboardLayout,
  dashboardLayoutStorageKey,
  DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS,
} from "./useDashboardLayout";
vi.mock("@/hooks/useAuth", () => ({ useAuth: () => ({ user: { id: 1 } }) }));
vi.mock("@/hooks/queries/admin/dashboardLayout", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/hooks/queries/admin/dashboardLayout")>();
  return {
    ...actual,
    useAdminDashboardLayout: () => ({
      data: {
        layout: { version: 1, entries: [{ id: "users", span: 5, rows: 4 }] },
        updated_at: null,
        etag: '"A"',
      },
      isSuccess: true,
    }),
  };
});
beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  sessionStorage.clear();
  setAccessToken("synthetic");
  setProfileId("profile-a");
  setProfileToken("pin-a");
  localStorage.setItem(
    dashboardLayoutStorageKey(1),
    JSON.stringify({ version: 1, entries: [{ id: "users", span: 5, rows: 4 }] }),
  );
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
function fixture() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: 3 } },
  });
  return {
    wrapper: ({ children }: { children: ReactNode }) =>
      createElement(QueryClientProvider, { client }, children),
  };
}
it.each(["timer", "unmount"])(
  "rejects original edit authority after replacement before %s flush",
  async (how) => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(null, { status: 204, headers: { ETag: '"B"' } }));
    vi.stubGlobal("fetch", fetchMock);
    const { result, unmount } = renderHook(useDashboardLayout, fixture());
    act(() => result.current.resizeWidget("users", { span: 6 }));
    act(() => setProfileToken("pin-b"));
    if (how === "unmount") unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS + 1);
    });
    expect(fetchMock).not.toHaveBeenCalled();
  },
);
it("submits the captured edit once after debounce", async () => {
  const fetchMock = vi
    .fn()
    .mockResolvedValue(new Response(null, { status: 204, headers: { ETag: '"B"' } }));
  vi.stubGlobal("fetch", fetchMock);
  const { result } = renderHook(useDashboardLayout, fixture());
  act(() => result.current.resizeWidget("users", { span: 6 }));
  expect(fetchMock).not.toHaveBeenCalled();
  await act(async () => {
    await vi.advanceTimersByTimeAsync(DASHBOARD_LAYOUT_SAVE_DEBOUNCE_MS + 1);
  });
  expect(fetchMock).toHaveBeenCalledOnce();
  expect(fetchMock.mock.calls[0]?.[1].method).toBe("PUT");
  expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1].body)).layout.entries).toEqual([
    { id: "users", span: 6, rows: 4 },
  ]);
});
