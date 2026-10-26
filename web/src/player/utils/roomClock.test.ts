// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  addRoomClockSample,
  roomClockOffsetMs,
  roomClockPingDelayMs,
  roomClockSample,
  type RoomClockSample,
} from "./roomClock";

describe("roomClockSample", () => {
  it("excludes server processing time from the round trip", () => {
    // Server clock 1 s ahead, 40 ms each way, 20 ms spent on the server.
    expect(roomClockSample(0, 1_040, 1_060, 100)).toEqual({ rttMs: 80, offsetMs: 1_000 });
  });

  it("drops round trips that are negative or longer than five seconds", () => {
    expect(roomClockSample(1_000, 2_000, 2_500, 1_200)).toBeNull();
    expect(roomClockSample(1_000, 2_000, 2_003, 1_000)).toBeNull();
    expect(roomClockSample(0, 0, 0, 5_001)).toBeNull();
    expect(roomClockSample(0, 0, 0, 5_000)).toEqual({ rttMs: 5_000, offsetMs: -2_500 });
    expect(roomClockSample(Number.NaN, 0, 0, 10)).toBeNull();
  });

  it("counts a round trip lost to millisecond truncation as zero", () => {
    // Each timestamp truncates up to 1 ms, so a LAN exchange can read -2 ms.
    expect(roomClockSample(1_000, 2_000, 2_002, 1_000)).toEqual({ rttMs: 0, offsetMs: 1_001 });
  });
});

describe("roomClockOffsetMs", () => {
  const sample = (rttMs: number, offsetMs: number): RoomClockSample => ({ rttMs, offsetMs });

  it("uses the minimum-RTT sample, not the latest", () => {
    let samples: RoomClockSample[] = [];
    expect(roomClockOffsetMs(samples)).toBeNull();
    samples = addRoomClockSample(samples, sample(300, 900));
    samples = addRoomClockSample(samples, sample(40, 1_000));
    samples = addRoomClockSample(samples, sample(600, 1_250));
    expect(roomClockOffsetMs(samples)).toBe(1_000);
  });

  it("forgets the best sample once eight newer ones arrive", () => {
    let samples = addRoomClockSample([], sample(10, 1_000));
    for (let i = 0; i < 7; i++) samples = addRoomClockSample(samples, sample(100 + i, 1_020 + i));
    expect(roomClockOffsetMs(samples)).toBe(1_000);
    samples = addRoomClockSample(samples, sample(200, 1_040));
    expect(samples).toHaveLength(8);
    expect(roomClockOffsetMs(samples)).toBe(1_020);
  });

  it("starts over when a sample shows the clock stepped", () => {
    let samples = addRoomClockSample([], sample(20, 1_000));
    samples = addRoomClockSample(samples, sample(300, 1_150));
    // Within both half round trips plus slack: the same clock.
    expect(roomClockOffsetMs(samples)).toBe(1_000);
    expect(samples).toHaveLength(2);
    // 40 ms round trips cannot both be right 200 ms apart: the clock moved,
    // and the slower new sample still wins over the stale fast one.
    samples = addRoomClockSample(samples, sample(40, 1_200));
    expect(samples).toEqual([sample(40, 1_200)]);
    expect(roomClockOffsetMs(samples)).toBe(1_200);
  });

  it("prefers the newer of two equally fast samples", () => {
    const samples = addRoomClockSample([sample(50, 1_000)], sample(50, 1_010));
    expect(roomClockOffsetMs(samples)).toBe(1_010);
  });
});

describe("roomClockPingDelayMs", () => {
  it("pings every 2 s until a connection has four samples, then every 15 s", () => {
    expect([0, 1, 2, 3, 4, 9].map(roomClockPingDelayMs)).toEqual([
      2_000, 2_000, 2_000, 2_000, 15_000, 15_000,
    ]);
  });
});
