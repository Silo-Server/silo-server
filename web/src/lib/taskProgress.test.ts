import { describe, expect, it } from "vitest";
import { clampTaskProgress, formatTaskProgress } from "./taskProgress";

describe("task progress formatting", () => {
  it("keeps fractional queue progress visible", () => {
    expect(formatTaskProgress(89.98)).toBe("90.0%");
    expect(formatTaskProgress(90.18)).toBe("90.2%");
  });

  it("clamps invalid and out-of-range progress", () => {
    expect(clampTaskProgress(Number.NaN)).toBe(0);
    expect(formatTaskProgress(-1)).toBe("0%");
    expect(formatTaskProgress(101)).toBe("100%");
  });
});
