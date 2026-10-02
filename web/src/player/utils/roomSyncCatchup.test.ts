import { describe, expect, it } from "vitest";
import {
  abandonRoomReload,
  beginRoomReload,
  createRoomReloadBudget,
  decideRoomCatchup,
  isNativePositionInRanges,
  landRoomReload,
  noteRoomReloadLoading,
  roomMembersLeftBehind,
  roomReloadAllowed,
  roomReloadLanded,
  roomReloadMaxIntervalMs,
  roomReloadMaxLeadSeconds,
  roomReloadMinIntervalMs,
  roomReloadStaleMs,
  settleRoomReloads,
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

describe("isNativePositionInRanges", () => {
  it("finds a position inside a single range", () => {
    expect(isNativePositionInRanges(seekableRanges([[10, 20]]), 15)).toBe(true);
  });

  it("rejects a position outside every range", () => {
    expect(isNativePositionInRanges(seekableRanges([[10, 20]]), 25)).toBe(false);
  });

  it("finds a position in a later range", () => {
    expect(
      isNativePositionInRanges(
        seekableRanges([
          [0, 5],
          [30, 40],
        ]),
        35,
      ),
    ).toBe(true);
  });

  it("rejects when there are no ranges", () => {
    expect(isNativePositionInRanges(seekableRanges([]), 0)).toBe(false);
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

describe("room rebuild budget", () => {
  it("runs one rebuild at a time and aims the next ahead by the measured startup", () => {
    const budget = createRoomReloadBudget();
    expect(roomReloadAllowed(budget, 0)).toBe(true);
    expect(beginRoomReload(budget, 100, 0)).toBe(100);
    expect(roomReloadAllowed(budget, 5_000)).toBe(false);

    // Until the element starts loading, even the target position belongs to
    // the stream being replaced.
    expect(roomReloadLanded(budget, 100)).toBe(false);
    noteRoomReloadLoading(budget);
    expect(roomReloadLanded(budget, 95)).toBe(false);
    expect(roomReloadLanded(budget, 100)).toBe(true);
    landRoomReload(budget, 6_000);
    expect(budget.leadSeconds).toBe(6);
    expect(roomReloadAllowed(budget, 6_000 + roomReloadMinIntervalMs - 1)).toBe(false);
    expect(roomReloadAllowed(budget, 6_000 + roomReloadMinIntervalMs)).toBe(true);
    expect(beginRoomReload(budget, 200, 20_000)).toBe(206);
  });

  it("gives every reload its own generation", () => {
    const budget = createRoomReloadBudget();
    beginRoomReload(budget, 100, 0);
    const first = budget.generation;
    abandonRoomReload(budget, 1_000);
    beginRoomReload(budget, 100, 40_000);
    const second = budget.generation;
    expect(second).not.toBe(first);
    // Converging does not end an in-flight reload.
    settleRoomReloads(budget);
    expect(budget.generation).toBe(second);
    noteRoomReloadLoading(budget);
    landRoomReload(budget, 42_000);
    expect(budget.generation).not.toBe(second);
  });

  it("never aims the load-time lead past the end of the media", () => {
    const budget = createRoomReloadBudget();
    beginRoomReload(budget, 100, 0);
    noteRoomReloadLoading(budget);
    landRoomReload(budget, 8_000);
    expect(budget.leadSeconds).toBe(8);

    expect(beginRoomReload(budget, 5_995, 60_000, 6_000)).toBe(6_000);
    expect(beginRoomReload(budget, 5_000, 120_000, 6_000)).toBe(5_008);
    // A room already past the reported end keeps its own position.
    expect(beginRoomReload(budget, 6_010, 180_000, 6_000)).toBe(6_010);
  });

  it("does not settle a backward reload on the stream it replaces", () => {
    const budget = createRoomReloadBudget();
    beginRoomReload(budget, 100, 0);
    noteRoomReloadLoading(budget);
    // Playback ahead of an earlier target is the old stream, not the reload.
    expect(roomReloadLanded(budget, 105)).toBe(false);
    expect(roomReloadLanded(budget, 100.4)).toBe(true);
  });

  it("backs off between rebuilds until the viewer converges", () => {
    const budget = createRoomReloadBudget();
    let now = 0;
    const intervals: number[] = [];
    for (let attempt = 0; attempt < 5; attempt++) {
      beginRoomReload(budget, 100, now);
      landRoomReload(budget, now);
      intervals.push(budget.nextAllowedAtMs - now);
      now = budget.nextAllowedAtMs;
    }
    expect(intervals).toEqual([10_000, 20_000, 40_000, 60_000, 60_000]);
    expect(Math.max(...intervals)).toBe(roomReloadMaxIntervalMs);

    settleRoomReloads(budget);
    expect(roomReloadAllowed(budget, now)).toBe(true);
    beginRoomReload(budget, 100, now);
    landRoomReload(budget, now);
    expect(budget.nextAllowedAtMs - now).toBe(roomReloadMinIntervalMs);
  });

  it("bounds the lead and stops an unlanded rebuild from blocking forever", () => {
    const budget = createRoomReloadBudget();
    beginRoomReload(budget, 100, 0);
    landRoomReload(budget, 60_000);
    expect(budget.leadSeconds).toBe(roomReloadMaxLeadSeconds);

    beginRoomReload(budget, 100, 100_000);
    expect(roomReloadAllowed(budget, 100_000 + roomReloadStaleMs - 1)).toBe(false);
    expect(roomReloadAllowed(budget, 100_000 + roomReloadStaleMs)).toBe(true);

    abandonRoomReload(budget, 200_000);
    expect(budget.targetSeconds).toBeNull();
    expect(roomReloadAllowed(budget, 200_000)).toBe(false);
  });
});

describe("roomMembersLeftBehind", () => {
  const member = (
    name: string,
    status: { ready?: boolean; syncing?: boolean; self?: boolean },
  ) => ({
    user_id: name.length,
    profile_id: name,
    display_name: name,
    is_self: status.self ?? false,
    is_ready: status.ready,
    is_syncing: status.syncing,
  });
  const waiting = {
    phase: "playing",
    playback_state: "waiting",
    selection_revision: 1,
    members: [
      member("Ann", { syncing: true }),
      member("Bob", { syncing: true }),
      member("Me", { syncing: true, self: true }),
    ],
  };

  it("names the other viewers a waiting deadline skipped", () => {
    const resumed = {
      ...waiting,
      playback_state: "playing",
      members: [member("Ann", { ready: true }), member("Bob", {}), member("Me", { self: true })],
    };
    expect(roomMembersLeftBehind(waiting, resumed)).toEqual(["Bob"]);
  });

  it("ignores rooms that paused or changed selection", () => {
    const skipped = [member("Ann", {}), member("Bob", {})];
    expect(
      roomMembersLeftBehind(waiting, { ...waiting, playback_state: "paused", members: skipped }),
    ).toEqual([]);
    expect(
      roomMembersLeftBehind(waiting, {
        ...waiting,
        playback_state: "playing",
        selection_revision: 2,
        members: skipped,
      }),
    ).toEqual([]);
  });
});
