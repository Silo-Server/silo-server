import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { expect, it, vi } from "vitest";
import { useAdminItemTrickplay } from "./trickplay";

const request = vi.hoisted(() => vi.fn());
vi.mock("@/api/v2/request", () => ({ v2: request }));

it.each(["pending", "running"])(
  "polls %s item work until publication and stops when disabled",
  async (state) => {
    vi.useFakeTimers();
    let current = state;
    request.mockImplementation(async () => ({
      files: [{ file_id: "1", state: current, failures: 0, servable: current === "ready" }],
    }));
    const client = new QueryClient();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    const view = renderHook(({ enabled }) => useAdminItemTrickplay("movie", { enabled }), {
      wrapper,
      initialProps: { enabled: true },
    });
    try {
      await act(() => vi.advanceTimersByTimeAsync(20));
      expect(view.result.current.data?.[0]?.state).toBe(state);
      current = "ready";
      await act(() => vi.advanceTimersByTimeAsync(5_000));
      expect(view.result.current.data?.[0]?.state).toBe("ready");
      expect(request).toHaveBeenCalledTimes(2);
      await act(() => vi.advanceTimersByTimeAsync(60_000));
      expect(request).toHaveBeenCalledTimes(2);
      current = "running";
      await act(() => view.result.current.refetch());
      view.rerender({ enabled: false });
      const count = request.mock.calls.length;
      await act(() => vi.advanceTimersByTimeAsync(60_000));
      expect(request).toHaveBeenCalledTimes(count);
    } finally {
      view.unmount();
      client.clear();
      request.mockReset();
      vi.useRealTimers();
    }
  },
);
