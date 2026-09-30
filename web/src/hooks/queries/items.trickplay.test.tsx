import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { V2ProblemError } from "@/api/v2/request";
import { useWatchTrickplay } from "./items";
const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", async (original) => ({
  ...(await original<typeof import("@/api/v2/request")>()),
  v2: request,
}));
afterEach(() => {
  vi.useRealTimers();
  request.mockReset();
});
const manifest = {
  interval_ms: 10000,
  thumbnail_width: 300,
  thumbnail_height: 168,
  tile_columns: 10,
  tile_rows: 10,
  thumbnail_count: 100,
  expires_at: new Date(Date.now() + 3600000).toISOString(),
  sheets: [{ index: 0, url: "https://example.com/sheet.jpg" }],
};
function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retryDelay: 1 } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { ...renderHook(() => useWatchTrickplay("movie", 1, true), { wrapper }), client };
}
it("recovers a transient initial manifest failure without a focus event", async () => {
  vi.useFakeTimers();
  request.mockRejectedValueOnce(new TypeError("network unavailable")).mockResolvedValue(manifest);
  const view = setup();
  await act(() => vi.advanceTimersByTimeAsync(20));
  expect(request).toHaveBeenCalledTimes(2);
  expect(view.result.current.data?.count).toBe(100);
  view.unmount();
  view.client.clear();
});
it("bounds immediate retries then polls for recovery", async () => {
  vi.useFakeTimers();
  request.mockRejectedValue(new TypeError("network unavailable"));
  const view = setup();
  await act(() => vi.advanceTimersByTimeAsync(20));
  expect(request).toHaveBeenCalledTimes(3);
  request.mockResolvedValue(manifest);
  await act(() => vi.advanceTimersByTimeAsync(60000));
  expect(view.result.current.data?.count).toBe(100);
  expect(request).toHaveBeenCalledTimes(4);
  view.unmount();
  view.client.clear();
});
it("stops requesting a manifest that is no longer available", async () => {
  vi.useFakeTimers();
  request.mockRejectedValue(
    new V2ProblemError("getWatchTrickplay", {
      type: "https://example.com/not_found",
      title: "Not found",
      status: 404,
      detail: "No previews",
      instance: "/api/v2/watch/files/1/trickplay",
    }),
  );
  const view = setup();
  await act(() => vi.advanceTimersByTimeAsync(120000));
  expect(request).toHaveBeenCalledTimes(1);
  view.unmount();
  view.client.clear();
});
