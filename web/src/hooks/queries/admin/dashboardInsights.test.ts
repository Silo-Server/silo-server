import { describe, expect, it } from "vitest";

import { adminKeys } from "../keys";
import {
  normalizeDownloadsTopLimit,
  normalizeInsightHours,
  normalizeTopActivityDays,
} from "./dashboardInsights";

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
