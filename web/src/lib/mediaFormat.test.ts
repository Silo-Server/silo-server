// @vitest-environment node

import { describe, expect, it } from "vitest";
import { titleOnlyNamesAudioFormat } from "./mediaFormat";

// The scanner stores FFprobe's codec long name as the title of an audio track
// that has no title tag (#4568). Displays drop such a title and show the
// language; a real title, numbers alone included, stays.
describe("titleOnlyNamesAudioFormat", () => {
  it.each([
    ["ATSC A/52B (AC-3, E-AC-3)", true],
    ["AAC (Advanced Audio Coding)", true],
    ["MP2 (MPEG audio layer 2)", true],
    ["PCM 32-bit floating point little-endian", true],
    ["DTS-HD MA 5.1", true],
    ["1", false],
    ["French (France)", false],
    ["Point of View", false],
    ["", false],
  ])("%j -> %s", (title, want) => {
    expect(titleOnlyNamesAudioFormat(title)).toBe(want);
  });

  it("checks a long title of repeated layouts without backtracking", () => {
    const started = performance.now();
    expect(titleOnlyNamesAudioFormat("5.1".repeat(5000) + "x")).toBe(false);
    expect(performance.now() - started).toBeLessThan(1000);
  });
});
