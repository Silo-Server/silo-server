import { describe, expect, it } from "vitest";
import {
  decideRoomCatchup,
  isNativePositionSeekable,
  roomCatchupBandSeconds,
  roomCatchupDeadbandSeconds,
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
