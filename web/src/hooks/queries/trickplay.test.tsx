import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { V2ProblemError, V2TimeoutError } from "@/api/v2/request";
import { useWatchTrickplay } from "./trickplay";
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
  await act(() => vi.advanceTimersByTimeAsync(3_100));
  expect(request).toHaveBeenCalledTimes(2);
  expect(view.result.current.data?.count).toBe(100);
  view.unmount();
  view.client.clear();
});
it("recovers a manifest request the server did not answer in time", async () => {
  vi.useFakeTimers();
  request
    .mockRejectedValueOnce(new V2TimeoutError("getWatchTrickplay", 30_000))
    .mockResolvedValue(manifest);
  const view = setup();
  await act(() => vi.advanceTimersByTimeAsync(3_100));
  expect(request).toHaveBeenCalledTimes(2);
  expect(view.result.current.data?.count).toBe(100);
  view.unmount();
  view.client.clear();
});
it("bounds immediate retries then polls for recovery", async () => {
  vi.useFakeTimers();
  request.mockRejectedValue(new TypeError("network unavailable"));
  const view = setup();
  await act(() => vi.advanceTimersByTimeAsync(3_100));
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

it("waits for Retry-After before retrying a rate-limited manifest", async () => {
  vi.useFakeTimers();
  request
    .mockRejectedValueOnce(
      new V2ProblemError(
        "getWatchTrickplay",
        {
          type: "https://example.com/rate_limited",
          title: "Rate limited",
          status: 429,
          detail: "Try later",
          instance: "/api/v2/watch/movie/trickplay",
        },
        10,
      ),
    )
    .mockResolvedValue(manifest);
  const view = setup();
  try {
    await act(() => vi.advanceTimersByTimeAsync(9_999));
    expect(request).toHaveBeenCalledTimes(1);
    await act(() => vi.advanceTimersByTimeAsync(20));
    expect(request).toHaveBeenCalledTimes(2);
    expect(view.result.current.data?.count).toBe(100);
  } finally {
    view.unmount();
    view.client.clear();
  }
});

it("polls a rate-limited manifest after bounded retries and honors a long Retry-After", async () => {
  vi.useFakeTimers();
  request.mockRejectedValue(
    new V2ProblemError(
      "getWatchTrickplay",
      {
        type: "https://example.com/rate_limited",
        title: "Rate limited",
        status: 429,
        detail: "Try later",
        instance: "/api/v2/watch/movie/trickplay",
      },
      90,
    ),
  );
  const view = setup();
  try {
    await act(() => vi.advanceTimersByTimeAsync(180_010));
    expect(request).toHaveBeenCalledTimes(3);
    request.mockResolvedValue(manifest);
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(request).toHaveBeenCalledTimes(3);
    await act(() => vi.advanceTimersByTimeAsync(30_000));
    expect(request).toHaveBeenCalledTimes(4);
    expect(view.result.current.data?.count).toBe(100);
  } finally {
    view.unmount();
    view.client.clear();
  }
});
