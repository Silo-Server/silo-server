import { describe, expect, it } from "vitest";
import {
  nextSubtitleRetryDelay,
  planSubtitleWindow,
  SUBTITLE_FETCH_RETRY_MAX_BACKOFF_MS,
  type SubtitleWindowState,
} from "./subtitleWindows";

const covered: SubtitleWindowState = {
  hasFetched: true,
  coverageStart: 100,
  windowEnd: 700,
  atEOF: false,
  inflightStart: null,
  failedAt: 0,
  retryDelayMs: 0,
};

describe("planSubtitleWindow", () => {
  it("starts fresh before anything is loaded and after a seek out of coverage", () => {
    expect(planSubtitleWindow({ ...covered, hasFetched: false }, 50, 0, 2)).toEqual({
      kind: "fresh",
      start: 48,
    });
    expect(planSubtitleWindow(covered, 90, 0, 10)).toEqual({ kind: "fresh", start: 80 });
    expect(planSubtitleWindow(covered, 800, 0, 2)).toEqual({ kind: "fresh", start: 798 });
  });

  it("extends near the end of coverage unless at end of file", () => {
    expect(planSubtitleWindow(covered, 680, 0, 2)).toEqual({ kind: "extend", start: 695 });
    expect(planSubtitleWindow({ ...covered, atEOF: true }, 680, 0, 2)).toEqual({ kind: "none" });
    expect(planSubtitleWindow(covered, 300, 0, 2)).toEqual({ kind: "none" });
  });

  it("waits for a window on the wire that covers the playhead, and replaces one that does not", () => {
    expect(planSubtitleWindow({ ...covered, inflightStart: 695 }, 690, 0, 2)).toEqual({
      kind: "none",
    });
    expect(planSubtitleWindow({ ...covered, inflightStart: 695 }, 2000, 0, 2)).toEqual({
      kind: "fresh",
      start: 1998,
    });
  });

  it("holds off inside a failure backoff", () => {
    const failing = { ...covered, hasFetched: false, failedAt: 1_000, retryDelayMs: 5_000 };
    expect(planSubtitleWindow(failing, 10, 3_000, 2)).toEqual({ kind: "none" });
    expect(planSubtitleWindow(failing, 10, 7_000, 2).kind).toBe("fresh");
  });
});

describe("nextSubtitleRetryDelay", () => {
  it("doubles up to the cap", () => {
    expect(nextSubtitleRetryDelay(0)).toBe(5_000);
    expect(nextSubtitleRetryDelay(5_000)).toBe(10_000);
    expect(nextSubtitleRetryDelay(40_000)).toBe(SUBTITLE_FETCH_RETRY_MAX_BACKOFF_MS);
  });
});
