import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useAdminLiveSessions } from "./stats";

const mocks = vi.hoisted(() => ({ api: vi.fn(), active: true }));
vi.mock("@/api/client", () => ({ api: mocks.api }));
vi.mock("@/hooks/usePageActivity", () => ({
  usePageActivity: () => ({ canPollDashboard: mocks.active }),
}));

beforeEach(() => {
  vi.useFakeTimers();
  mocks.active = true;
  mocks.api.mockReset();
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

it.each([false, true])(
  "refreshes the includeHidden=%s view without playback events and pauses offscreen",
  async (includeHidden) => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    );
    mocks.api.mockResolvedValueOnce({ sessions: [{ session_id: "ended" }] });
    mocks.api.mockResolvedValue({ sessions: [] });
    const { result, rerender } = renderHook(() => useAdminLiveSessions(includeHidden), { wrapper });
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(result.current.data?.sessions).toHaveLength(1);

    await act(() => vi.advanceTimersByTimeAsync(30_000));
    expect(result.current.data?.sessions).toHaveLength(0);
    expect(mocks.api).toHaveBeenCalledTimes(2);

    mocks.active = false;
    rerender();
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(mocks.api).toHaveBeenCalledTimes(2);

    mocks.active = true;
    rerender();
    await act(() => vi.advanceTimersByTimeAsync(30_000));
    expect(mocks.api).toHaveBeenCalledTimes(3);
    client.clear();
  },
);
