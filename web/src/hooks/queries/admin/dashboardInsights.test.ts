import { createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
}));

vi.mock("@/api/client", () => ({
  api: mocks.api,
}));

import { adminKeys } from "../keys";
import {
  adminDownloadsStatsPath,
  adminPlaybackActivityPath,
  adminTimeseriesPath,
  adminTopActivityPath,
  normalizeDownloadsTopLimit,
  normalizeInsightHours,
  normalizeTopActivityDays,
  useAdminDownloadsStats,
  useAdminPlaybackActivity,
  useAdminTimeseries,
  useAdminTopActivity,
} from "./dashboardInsights";

function createQueryClient() {
  return new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
}

function createWrapper(queryClient: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client: queryClient }, children);
  };
}

describe("dashboard insight windows", () => {
  it.each([
    [24, 24],
    [1, 1],
    [168, 168],
    [744, 744],
    [0, 1],
    [-5, 1],
    [500, 500],
    [5_000, 744],
    [24.9, 24],
    [Number.NaN, 24],
  ])("clamps %p hours to %p", (input, expected) => {
    expect(normalizeInsightHours(input)).toBe(expected);
  });

  it.each([
    [7, 7],
    [1, 1],
    [30, 30],
    [0, 1],
    [90, 30],
    [Number.NaN, 7],
  ])("clamps %p days to %p", (input, expected) => {
    expect(normalizeTopActivityDays(input)).toBe(expected);
  });

  it.each([
    [10, 10],
    [1, 1],
    [25, 25],
    [0, 1],
    [99, 25],
    [Number.NaN, 10],
  ])("clamps a top limit of %p to %p", (input, expected) => {
    expect(normalizeDownloadsTopLimit(input)).toBe(expected);
  });

  it("builds request paths from the clamped window", () => {
    expect(adminTimeseriesPath(24)).toBe("/admin/stats/timeseries?hours=24");
    expect(adminTimeseriesPath(1)).toBe("/admin/stats/timeseries?hours=1");
    expect(adminTimeseriesPath(9_999)).toBe("/admin/stats/timeseries?hours=744");
    expect(adminPlaybackActivityPath(24)).toBe("/admin/stats/playback-activity?hours=24");
    expect(adminTopActivityPath(7)).toBe("/admin/stats/top-activity?days=7");
    expect(adminTopActivityPath(0)).toBe("/admin/stats/top-activity?days=1");
    expect(adminDownloadsStatsPath(10)).toBe("/admin/stats/downloads?limit=10");
    expect(adminDownloadsStatsPath(99)).toBe("/admin/stats/downloads?limit=25");
  });

  it("keys every window under the prefix the dashboard refresh invalidates", () => {
    const roots = [
      [adminKeys.dashboardTimeseriesRoot(), adminKeys.dashboardTimeseries(24)],
      [adminKeys.playbackActivityRoot(), adminKeys.playbackActivity(24)],
      [adminKeys.topActivityRoot(), adminKeys.topActivity(7)],
      [adminKeys.downloadsStatsRoot(), adminKeys.downloadsStats(10)],
    ] as const;
    for (const [root, leaf] of roots) {
      expect(leaf.slice(0, root.length)).toEqual([...root]);
    }
  });
});

describe("dashboard insight hooks", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.api.mockResolvedValue({});
  });

  it("requests the timeseries window it was asked for and caches it by window", async () => {
    const queryClient = createQueryClient();
    const { result } = renderHook(() => useAdminTimeseries(1), {
      wrapper: createWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(mocks.api).toHaveBeenCalledWith("/admin/stats/timeseries?hours=1");
    expect(queryClient.getQueryData(adminKeys.dashboardTimeseries(1))).toEqual({});
    expect(queryClient.getQueryData(adminKeys.dashboardTimeseries(24))).toBeUndefined();
  });

  it("defaults playback activity to a 24 hour window", async () => {
    const queryClient = createQueryClient();
    const { result } = renderHook(() => useAdminPlaybackActivity(), {
      wrapper: createWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(mocks.api).toHaveBeenCalledWith("/admin/stats/playback-activity?hours=24");
    expect(queryClient.getQueryData(adminKeys.playbackActivity(24))).toEqual({});
  });

  it("defaults top activity to a 7 day window", async () => {
    const queryClient = createQueryClient();
    const { result } = renderHook(() => useAdminTopActivity(), {
      wrapper: createWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(mocks.api).toHaveBeenCalledWith("/admin/stats/top-activity?days=7");
    expect(queryClient.getQueryData(adminKeys.topActivity(7))).toEqual({});
  });

  it("defaults downloads stats to a top-10 list", async () => {
    const queryClient = createQueryClient();
    const { result } = renderHook(() => useAdminDownloadsStats(), {
      wrapper: createWrapper(queryClient),
    });

    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(mocks.api).toHaveBeenCalledWith("/admin/stats/downloads?limit=10");
    expect(queryClient.getQueryData(adminKeys.downloadsStats(10))).toEqual({});
  });

  it("paces from the dashboard loop: stale just under 60s, never self-polling", async () => {
    const queryClient = createQueryClient();
    const wrapper = createWrapper(queryClient);
    renderHook(() => useAdminTimeseries(24), { wrapper });
    renderHook(() => useAdminPlaybackActivity(24), { wrapper });
    renderHook(() => useAdminTopActivity(7), { wrapper });

    await waitFor(() => expect(queryClient.getQueryCache().getAll()).toHaveLength(3));

    const staleTimes = new Map(
      queryClient
        .getQueryCache()
        .getAll()
        .map((query) => [
          JSON.stringify(query.queryKey),
          query.options as { staleTime?: number; refetchInterval?: unknown },
        ]),
    );

    expect(staleTimes.get(JSON.stringify(adminKeys.dashboardTimeseries(24)))?.staleTime).toBe(
      55_000,
    );
    expect(staleTimes.get(JSON.stringify(adminKeys.playbackActivity(24)))?.staleTime).toBe(55_000);
    expect(staleTimes.get(JSON.stringify(adminKeys.topActivity(7)))?.staleTime).toBe(300_000);
    for (const options of staleTimes.values()) {
      expect(options.refetchInterval).toBeUndefined();
    }
  });
});
