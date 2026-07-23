import { describe, expect, it } from "vitest";

import { mediaDurationSeconds, mediaElementDuration, toMediaTime, toPlayerTime } from "./mediaTimeline";

describe("media timeline", () => {
  it("round-trips a position through a stream origin", () => {
    expect(toMediaTime(60, 3000)).toBe(3060);
    expect(toPlayerTime(3060, 3000)).toBe(60);
  });

  it("prefers the server runtime over a produced-window duration", () => {
    expect(mediaDurationSeconds(5400, 120)).toBe(5400);
    expect(mediaElementDuration(2880, 3000)).toBeNull();
  });

  it("falls back safely when no backend runtime exists", () => {
    expect(mediaDurationSeconds(0, 120)).toBe(120);
    expect(mediaElementDuration(0, 240)).toBe(240);
    expect(mediaElementDuration(0, Number.POSITIVE_INFINITY)).toBeNull();
  });
});
