import { describe, expect, it } from "vitest";
import {
  decideRoomCatchup,
  isNativePositionSeekable,
  roomCatchupBandSeconds,
  roomCatchupConverged,
  roomCatchupDeadbandSeconds,
  roomCatchupExpectedPosition,
  roomCatchupMaxRate,
  roomCatchupMinRate,
} from "./roomSyncCatchup";

function seekableRanges(ranges: Array<[number, number]>): TimeRanges {
  return {
    length: ranges.length,
    start: (index: number) => ranges[index]?.[0] ?? 0,
    end: (index: number) => ranges[index]?.[1] ?? 0,
  } as TimeRanges;
}

describe("isNativePositionSeekable", () => {
  it("finds a position inside a single range", () => {
    expect(isNativePositionSeekable(seekableRanges([[10, 20]]), 15)).toBe(true);
  });

  it("rejects a position outside every range", () => {
    expect(isNativePositionSeekable(seekableRanges([[10, 20]]), 25)).toBe(false);
  });

  it("finds a position in a later range", () => {
    expect(
      isNativePositionSeekable(
        seekableRanges([
          [0, 5],
          [30, 40],
        ]),
        35,
      ),
    ).toBe(true);
  });

  it("rejects when there are no ranges", () => {
    expect(isNativePositionSeekable(seekableRanges([]), 0)).toBe(false);
  });
});

describe("decideRoomCatchup", () => {
  const base = {
    targetPositionSeconds: 100,
    localPositionSeconds: 100,
    targetLocallySeekable: false,
  };

  it("always seeks an explicit room seek", () => {
    expect(decideRoomCatchup({ ...base, action: "seek" })).toEqual({ kind: "seek" });
  });

  it("does nothing when playback already matches the room", () => {
    expect(
      decideRoomCatchup({
        ...base,
        action: "play",
        localPositionSeconds: base.targetPositionSeconds - roomCatchupDeadbandSeconds,
      }),
    ).toEqual({ kind: "none" });
  });

  it("keeps seekable targets a seek for every action", () => {
    expect(
      decideRoomCatchup({
        ...base,
        action: "play",
        targetLocallySeekable: true,
        localPositionSeconds: base.targetPositionSeconds - 1.5,
      }),
    ).toEqual({ kind: "seek" });
    expect(
      decideRoomCatchup({
        ...base,
        action: "pause",
        targetLocallySeekable: true,
        localPositionSeconds: base.targetPositionSeconds - 1.5,
      }),
    ).toEqual({ kind: "seek" });
  });

  it("never rebuilds a stream to park a paused member", () => {
    expect(
      decideRoomCatchup({
        ...base,
        action: "pause",
        localPositionSeconds: base.targetPositionSeconds - roomCatchupBandSeconds - 30,
      }),
    ).toEqual({ kind: "none" });
  });

  it("converges in-band out-of-window drift behind the room", () => {
    expect(
      decideRoomCatchup({
        ...base,
        action: "play",
        localPositionSeconds: base.targetPositionSeconds - 1,
      }),
    ).toEqual({ kind: "rate", rate: 1 + 1 / 8 });
  });

  it("caps the convergence rate at the band edge", () => {
    expect(
      decideRoomCatchup({
        ...base,
        action: "play",
        localPositionSeconds: base.targetPositionSeconds - roomCatchupBandSeconds,
      }),
    ).toEqual({ kind: "rate", rate: roomCatchupMaxRate });
  });

  it("clamps ahead drift to the slowest convergence rate", () => {
    expect(
      decideRoomCatchup({
        ...base,
        action: "play",
        localPositionSeconds: base.targetPositionSeconds + 1,
      }),
    ).toEqual({ kind: "rate", rate: roomCatchupMinRate });
  });

  it("seeks out-of-band drift", () => {
    expect(
      decideRoomCatchup({
        ...base,
        action: "play",
        localPositionSeconds: base.targetPositionSeconds - roomCatchupBandSeconds - 0.5,
      }),
    ).toEqual({ kind: "seek" });
  });
});

describe("room catch-up convergence", () => {
  const target = { positionSeconds: 100, executeAtMs: 1_000 };

  it("does not advance before the command executes", () => {
    expect(roomCatchupExpectedPosition(target, 500)).toBe(100);
    expect(roomCatchupExpectedPosition(target, 1_000)).toBe(100);
  });

  it("advances the room position at 1x after execution", () => {
    expect(roomCatchupExpectedPosition(target, 4_000)).toBe(103);
  });

  it("converges when playback reaches the advancing position", () => {
    // At 4s the room expects 103; playback at 102.8 is inside the deadband.
    expect(roomCatchupConverged(target, 102.8, 4_000)).toBe(true);
    expect(roomCatchupConverged(target, 102.0, 4_000)).toBe(false);
  });

  it("stops a slowed member once the advancing room position reaches it", () => {
    // A member ahead of the room moves away from the command's static
    // position; convergence has to come from the advancing position.
    expect(roomCatchupConverged(target, 102.8, 1_000)).toBe(false);
    expect(roomCatchupConverged(target, 102.8, 3_000)).toBe(false);
    expect(roomCatchupConverged(target, 102.8, 3_500)).toBe(true);
  });
});
