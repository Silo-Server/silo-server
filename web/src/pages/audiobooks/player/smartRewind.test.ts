import { describe, expect, it } from "vitest";
import { coldResumePosition, smartRewindSeconds } from "./smartRewind";

describe("smartRewindSeconds", () => {
  it("does not rewind for brief pauses", () => {
    expect(smartRewindSeconds(0)).toBe(0);
    expect(smartRewindSeconds(9_999)).toBe(0);
  });

  it("scales the rewind with the pause duration", () => {
    expect(smartRewindSeconds(10_000)).toBe(3);
    expect(smartRewindSeconds(59_999)).toBe(3);
    expect(smartRewindSeconds(60_000)).toBe(10);
    expect(smartRewindSeconds(599_999)).toBe(10);
    expect(smartRewindSeconds(600_000)).toBe(20);
    expect(smartRewindSeconds(3_599_999)).toBe(20);
    expect(smartRewindSeconds(3_600_000)).toBe(30);
    expect(smartRewindSeconds(86_400_000)).toBe(30);
  });

  it("treats invalid input as no rewind", () => {
    expect(smartRewindSeconds(NaN)).toBe(0);
    expect(smartRewindSeconds(-5)).toBe(0);
  });
});

describe("coldResumePosition", () => {
  it("rewinds a flat amount when enabled", () => {
    expect(coldResumePosition(120, true)).toBe(110);
  });

  it("never goes negative", () => {
    expect(coldResumePosition(4, true)).toBe(0);
  });

  it("leaves fresh starts and disabled prefs untouched", () => {
    expect(coldResumePosition(0, true)).toBe(0);
    expect(coldResumePosition(120, false)).toBe(120);
  });
});
