import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import AdminDashboard from "./AdminDashboard";

const mocks = vi.hoisted(() => ({
  updatedAt: 1,
  sessionsUpdatedAt: 1,
  refetch: vi.fn().mockResolvedValue({}),
  fetchStats: vi.fn(),
  client: { invalidateQueries: vi.fn().mockResolvedValue(undefined), setQueryData: vi.fn() },
}));
vi.mock("@tanstack/react-query", () => ({ useQueryClient: () => mocks.client }));
vi.mock("@/hooks/queries/admin/stats", () => ({
  useAdminStats: () => ({ data: {}, dataUpdatedAt: mocks.updatedAt, isStale: true }),
  useAdminLiveSessions: () => ({
    data: { sessions: [] },
    dataUpdatedAt: mocks.sessionsUpdatedAt,
    isStale: false,
  }),
  fetchAdminStats: mocks.fetchStats,
}));
vi.mock("@/hooks/queries/admin/libraries", () => ({
  useAdminLibraries: () => ({
    data: [],
    dataUpdatedAt: mocks.updatedAt,
    isStale: true,
    refetch: mocks.refetch,
  }),
  useScanAllLibraries: () => ({ isPending: false }),
}));
vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({
    data: [],
    dataUpdatedAt: mocks.updatedAt,
    isStale: true,
    refetch: mocks.refetch,
  }),
}));
vi.mock("@/hooks/queries/admin/plugins", () => ({
  useAdminPluginInstallations: () => ({ data: [] }),
}));
vi.mock("@/hooks/queries/admin/policy", () => ({ usePolicyCapability: () => ({}) }));
vi.mock("@/hooks/usePageActivity", () => ({ usePageActivity: () => ({ canPollDashboard: true }) }));
vi.mock("@/components/AdminSectionCommandDialog", () => ({
  AdminSectionCommandDialog: () => null,
}));
vi.mock("@/components/admin/dashboard/DashboardGrid", () => ({ DashboardGrid: () => null }));
vi.mock("@/components/admin/dashboard/useDashboardLayout", () => ({
  useDashboardLayout: () => ({ isCustomizing: false }),
}));

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-04T12:00:00Z"));
  vi.clearAllMocks();
  mocks.updatedAt = Date.now();
  mocks.sessionsUpdatedAt = Date.now();
  mocks.fetchStats.mockImplementation(async () => {
    mocks.updatedAt = Date.now();
    mocks.sessionsUpdatedAt = Date.now();
    return {};
  });
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

it("refreshes the whole dashboard across two intervals while sessions keep updating", async () => {
  const { rerender } = render(<AdminDashboard />);
  for (let tick = 1; tick <= 4; tick++) {
    await act(() => vi.advanceTimersByTimeAsync(30_000));
    mocks.sessionsUpdatedAt = Date.now();
    rerender(<AdminDashboard />);
    expect(mocks.fetchStats).toHaveBeenCalledTimes(Math.floor(tick / 2));
  }
});

it("refreshes both live-session visibility variants on manual refresh", async () => {
  render(<AdminDashboard />);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await act(() => vi.advanceTimersByTimeAsync(1_000));
  expect(mocks.client.invalidateQueries).toHaveBeenCalledWith({
    queryKey: ["admin", "sessions", "live"],
  });
  expect(mocks.fetchStats).toHaveBeenCalledTimes(1);
});

it("retries on the next interval after an automatic refresh fails", async () => {
  const error = vi.spyOn(console, "error").mockImplementation(() => {});
  mocks.fetchStats.mockRejectedValueOnce(new Error("temporary outage"));
  render(<AdminDashboard />);
  await act(() => vi.advanceTimersByTimeAsync(60_000));
  expect(mocks.fetchStats).toHaveBeenCalledTimes(1);
  expect(error).toHaveBeenCalledWith("Failed to refresh dashboard", expect.any(Error));
  await act(() => vi.advanceTimersByTimeAsync(60_000));
  expect(mocks.fetchStats).toHaveBeenCalledTimes(2);
  error.mockRestore();
});

it.each(["resolve", "reject"] as const)(
  "skips automatic refresh ticks until a pending request settles (%s)",
  async (outcome) => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    let resolve!: (value: object) => void;
    let reject!: (reason: Error) => void;
    mocks.fetchStats.mockReturnValueOnce(
      new Promise((res, rej) => {
        resolve = res;
        reject = rej;
      }),
    );
    try {
      render(<AdminDashboard />);
      await act(() => vi.advanceTimersByTimeAsync(180_000));
      expect(mocks.fetchStats).toHaveBeenCalledTimes(1);
      expect(screen.getByRole("button", { name: "Refresh" })).toBeEnabled();

      await act(async () => {
        if (outcome === "resolve") resolve({});
        else reject(new Error("temporary outage"));
      });
      expect(error).toHaveBeenCalledTimes(outcome === "reject" ? 1 : 0);
      await act(() => vi.advanceTimersByTimeAsync(60_000));
      expect(mocks.fetchStats).toHaveBeenCalledTimes(2);
    } finally {
      error.mockRestore();
    }
  },
);
