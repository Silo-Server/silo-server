import { describe, expect, it } from "vitest";

import {
  MAX_SAME_SEGMENT_FAILURES,
  MAX_STREAK_FAILURES,
  SegmentRetryBudget,
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
