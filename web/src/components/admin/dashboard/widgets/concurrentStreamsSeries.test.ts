import { describe, expect, it } from "vitest";

import type { AdminTimeseries, AdminTimeseriesPoint } from "@/api/types";
import { buildMethodBands, hasMethodSplit } from "./concurrentStreamsSeries";

const HOUR_MS = 3_600_000;
const TO = Date.parse("2026-10-29T12:00:00Z");
const AT = TO - 30 * 60_000;

function series(point: Partial<AdminTimeseriesPoint>): AdminTimeseries {
  return {
    resolution_seconds: 60,
    from: new Date(TO - HOUR_MS).toISOString(),
    to: new Date(TO).toISOString(),
    oldest_sample_at: new Date(TO - 2 * HOUR_MS).toISOString(),
    points: [
      {
        t: new Date(AT).toISOString(),
        streams: 0,
        direct: 0,
        remux: 0,
        transcode: 0,
        egress_kbps: 0,
        ...point,
      },
    ],
  };
}

function valuesAt(bands: ReturnType<typeof buildMethodBands>) {
  return Object.fromEntries(
    bands.map((band) => [band.key, band.points.find((point) => point.t === AT)?.value]),
  );
}

describe("buildMethodBands", () => {
  // The per-column peaks (direct 6, transcode 7) overshoot the total of 7;
  // the bands must come from the peak-minute split so they meet the total.
  it("stacks the peak-minute split, not the per-column peaks", () => {
    const bands = buildMethodBands(
      series({
        streams: 7,
        direct: 6,
        remux: 1,
        transcode: 7,
        streams_by_method: { direct: 6, remux: 1, transcode: 0 },
      }),
    );
    expect(valuesAt(bands)).toEqual({ direct: 6, remux: 1, transcode: 0 });
  });

  it("adds an Other band for streams no method accounts for", () => {
    const bands = buildMethodBands(
      series({ streams: 5, streams_by_method: { direct: 2, remux: 0, transcode: 1 } }),
    );
    expect(valuesAt(bands)).toEqual({ direct: 2, remux: 0, transcode: 1, other: 2 });
  });
});

describe("hasMethodSplit", () => {
  // A server without streams_by_method must not get a Stacked view that files
  // every stream under Other.
  it("is false when the server omits the split", () => {
    expect(hasMethodSplit(series({ streams: 5, direct: 5 }))).toBe(false);
    expect(
      hasMethodSplit(
        series({ streams: 5, streams_by_method: { direct: 5, remux: 0, transcode: 0 } }),
      ),
    ).toBe(true);
  });
});
