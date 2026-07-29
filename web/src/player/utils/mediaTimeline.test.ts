import { describe, expect, it } from "vitest";

import { mediaDurationSeconds, toMediaTime, toPlayerTime } from "./mediaTimeline";

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

describe("mediaDurationSeconds", () => {
  it("prefers the server runtime over the element duration", () => {
    expect(mediaDurationSeconds(5400, 120)).toBe(5400);
  });

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
