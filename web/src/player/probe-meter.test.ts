import { describe, expect, it } from "vitest";
import vectors from "../../../docs/design/schemas/buffering-quality/v1/vectors.json";
import {
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
