import { describe, expect, it } from "vitest";

import {
  bufferedAheadSeconds,
  mediaDurationSeconds,
  subtitleStartPositionSeconds,
  toMediaTime,
  toPlayerTime,
} from "./mediaTimeline";

describe("toMediaTime / toPlayerTime", () => {
  it("round-trips a position through a stream origin", () => {
    expect(toMediaTime(60, 3000)).toBe(3060);
    expect(toPlayerTime(3060, 3000)).toBe(60);
  });

  it("preserves an out-of-window target before the stream origin", () => {
    expect(toMediaTime(-10, 0)).toBe(0);
    expect(toPlayerTime(10, 3000)).toBe(-2990);
  });
});

describe("bufferedAheadSeconds", () => {
  const ranges = (...spans: Array<[number, number]>) =>
    ({
      length: spans.length,
      start: (i: number) => spans[i]![0],
      end: (i: number) => spans[i]![1],
    }) as unknown as TimeRanges;
  const buffered = ranges([0, 30], [30.05, 70], [100, 160]);

  it.each([
    { position: 10, ahead: 60, why: "across a hole small enough for hls.js to step over" },
    { position: 30, ahead: 40, why: "on that hole" },
    { position: 90, ahead: 0, why: "in a gap" },
    { position: 120, ahead: 40, why: "inside a later range" },
    { position: 160, ahead: 0, why: "at the end of the buffer" },
  ])("measures to the end of the buffer at $position ($why)", ({ position, ahead }) => {
    expect(bufferedAheadSeconds(buffered, position)).toBeCloseTo(ahead);
  });
});

describe("mediaDurationSeconds", () => {
  // The regression this function exists for: a copy remux resumed at 50
  // minutes reports a player-local duration covering only the produced
  // window. Pairing that with a media-time position of ~3060 would read as
  // "finished", latching the item watched and clearing its resume point.
  it("does not let a produced-window duration stand in for the runtime", () => {
    const positionSeconds = toMediaTime(60, 3000);
    const duration = mediaDurationSeconds(5400, 120);

    expect(duration).toBe(5400);
    expect(positionSeconds >= (duration ?? 0)).toBe(false);
  });

  it("falls back to the element duration only when the server has none", () => {
    expect(mediaDurationSeconds(0, 120)).toBe(120);
    expect(mediaDurationSeconds(null, 120)).toBe(120);
    expect(mediaDurationSeconds(undefined, 120)).toBe(120);
  });

  it("returns undefined when neither runtime is known, so callers omit it", () => {
    expect(mediaDurationSeconds(0, 0)).toBeUndefined();
    expect(mediaDurationSeconds(null, undefined)).toBeUndefined();
    expect(mediaDurationSeconds(undefined, NaN)).toBeUndefined();
  });
});

describe("subtitleStartPositionSeconds", () => {
  it("uses the resume anchor while media metadata is not ready", () => {
    const resumePosition = toMediaTime(60, 3000);

    expect(subtitleStartPositionSeconds(0, 0, resumePosition)).toBe(3060);
  });
});
