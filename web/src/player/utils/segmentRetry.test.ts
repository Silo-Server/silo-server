import { describe, expect, it } from "vitest";

import {
  MAX_SAME_SEGMENT_FAILURES,
  MAX_STARTUP_SEGMENT_FAILURES,
  MAX_STREAK_FAILURES,
  SegmentRetryBudget,
  StartupSegmentBudget,
  segmentRetryDelayMs,
} from "./segmentRetry";

describe("SegmentRetryBudget", () => {
  it("retries a transport hiccup once per segment", () => {
    const budget = new SegmentRetryBudget();
    expect(budget.recordFailure("frag:12")).toBe(0);
    // The load worked: the next blip starts a fresh streak.
    budget.reset();
    expect(budget.recordFailure("frag:48")).toBe(0);
  });

  it("stops after the same segment fails repeatedly", () => {
    const budget = new SegmentRetryBudget();
    expect(budget.recordFailure("frag:37")).toBe(0);
    expect(budget.recordFailure("frag:37")).toBeNull();
  });

  it("stops a source whose segments each fail at a new index", () => {
    const budget = new SegmentRetryBudget();
    const attempts = Array.from({ length: MAX_STREAK_FAILURES + 1 }, (_, index) =>
      budget.recordFailure(`frag:${index}`),
    );
    expect(attempts.slice(0, MAX_STREAK_FAILURES)).toEqual(
      Array.from({ length: MAX_STREAK_FAILURES }, (_, index) => index),
    );
    expect(attempts.at(-1)).toBeNull();
  });

  it("counts a playlist failure as one identity", () => {
    const budget = new SegmentRetryBudget();
    expect(budget.recordFailure(null)).toBe(0);
    expect(budget.recordFailure(null)).toBeNull();
  });

  it("treats the per-segment cap as the smaller of the two", () => {
    expect(MAX_SAME_SEGMENT_FAILURES).toBeLessThan(MAX_STREAK_FAILURES);
  });
});

describe("segmentRetryDelayMs", () => {
  it("backs off from one second and stops growing at the cap", () => {
    expect([0, 1, 2, 3, 10, -1].map(segmentRetryDelayMs)).toEqual([
      1_000, 2_000, 4_000, 8_000, 8_000, 1_000,
    ]);
  });
});

describe("StartupSegmentBudget", () => {
  it("backs a start that has drawn no frame off, then stops it", () => {
    const budget = new StartupSegmentBudget();

    const verdicts = Array.from({ length: MAX_STARTUP_SEGMENT_FAILURES }, () =>
      budget.recordFailure(false),
    );

    // The hold grows to the cap and never shrinks: each failure buys a longer
    // pause than the one before it, so a plan that keeps failing cannot fetch
    // at full rate.
    const delays = verdicts.map((verdict) => (verdict.kind === "hold" ? verdict.delayMs : -1));
    expect(delays).toEqual([1_000, 2_000, 4_000, 8_000, 8_000, 8_000, 8_000, 8_000, 8_000, 8_000]);
    delays.slice(1).forEach((delay, index) => {
      expect(delay).toBeGreaterThanOrEqual(delays[index] ?? 0);
    });

    // The budget is the hard cap on how many failed loads a never-started plan
    // can issue: past it the caller stops fetching instead of asking again.
    expect(budget.recordFailure(false)).toEqual({ kind: "stop" });
    expect(budget.recordFailure(false)).toEqual({ kind: "stop" });
  });

  it("keeps the ordinary retry cadence once a frame rendered", () => {
    const budget = new StartupSegmentBudget();

    for (let attempt = 0; attempt < MAX_STARTUP_SEGMENT_FAILURES * 3; attempt++) {
      expect(budget.recordFailure(true)).toEqual({ kind: "ignore" });
    }
  });
});
