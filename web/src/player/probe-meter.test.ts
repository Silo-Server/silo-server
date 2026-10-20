import { describe, expect, it } from "vitest";
import vectors from "../../../docs/design/schemas/buffering-quality/v1/vectors.json";
import {
  MAX_PROBE_KBPS,
  MIN_PROBE_KBPS,
  measureProbe,
  PROBE_FIRST_BYTE_DEADLINE_MS,
  PROBE_MODES,
  PROBE_WINDOW_MS,
  type ProbeMode,
} from "./probe-meter";

describe("speed test probe vectors", () => {
  it("uses the vectors' constants", () => {
    const c = vectors.probe_constants;
    expect(PROBE_WINDOW_MS).toBe(c.PROBE_WINDOW_MS);
    expect(PROBE_FIRST_BYTE_DEADLINE_MS).toBe(c.PROBE_FIRST_BYTE_DEADLINE_MS);
    expect(MIN_PROBE_KBPS).toBe(c.MIN_PROBE_KBPS);
    expect(MAX_PROBE_KBPS).toBe(c.MAX_PROBE_KBPS);
    expect(PROBE_MODES.quick).toEqual({
      bytes: c.QUICK_PROBE_BYTES,
      maxMs: c.QUICK_PROBE_MAX_MS,
      settleWindows: c.QUICK_SETTLE_WINDOWS,
    });
    expect(PROBE_MODES.accurate).toEqual({
      bytes: c.ACCURATE_PROBE_BYTES,
      maxMs: c.ACCURATE_PROBE_MAX_MS,
      settleWindows: c.ACCURATE_SETTLE_WINDOWS,
    });
  });

  for (const vector of vectors.probe_cases)
    it(vector.name, () => {
      const outcome = measureProbe(
        vector.mode as ProbeMode,
        "target_kbps" in vector ? vector.target_kbps : undefined,
        vector.chunks as [number, number][],
        "end" in vector ? vector.end : undefined,
      );
      if (vector.expect === null) {
        expect(outcome.result).toBeNull();
        return;
      }
      const { stop, ...result } = vector.expect;
      expect(outcome).toEqual({ stop, result });
    });
});

describe("measureProbe", () => {
  it("reports a whole transfer that ends between milliseconds in whole milliseconds", () => {
    const outcome = measureProbe(
      "quick",
      undefined,
      [
        [0, 60_000_000],
        [450.25, 40_000_000],
      ],
      900.4,
    );
    expect(outcome.result).toEqual({
      kbps: 888_888,
      lower_bound: true,
      bytes: 100_000_000,
      duration_ms: 900,
    });
  });

  it("measures nothing from a settled rate under the floor", () => {
    // 5,000 bytes in each window settle at 80 kbps.
    const chunks: [number, number][] = [0, 500, 1000, 1500, 2000, 2500].map((t) => [t, 5_000]);
    expect(measureProbe("quick", undefined, chunks).result).toBeNull();
  });

  // An administrator cap can set a target under the floor; reaching it proves
  // nothing a result may report.
  it("measures nothing when a quick test reaches a target under the floor", () => {
    const chunks: [number, number][] = [
      [0, 1_000_000],
      [600, 1_000_000],
      [1100, 1_000_000],
    ];
    expect(measureProbe("quick", 63, chunks)).toEqual({ stop: 1000, result: null });
  });

  it("measures nothing when a whole transfer reaches a target under the floor", () => {
    const outcome = measureProbe(
      "quick",
      63,
      [
        [0, 60_000_000],
        [400, 40_000_000],
      ],
      600,
    );
    expect(outcome.result).toBeNull();
  });
});
