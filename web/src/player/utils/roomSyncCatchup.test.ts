// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  abandonRoomReload,
  beginRoomReload,
  createRoomReloadBudget,
  createRoomSeekCorrector,
  decideRoomCommand,
  expireRoomReload,
  isNativePositionInRanges,
  landRoomReload,
  nextRoomNudgeRate,
  noteRoomCorrectorSeek,
  noteRoomReloadLoading,
  observeRoomCorrector,
  roomCorrectorSeekDue,
  roomCorrectorSeekTarget,
  roomLoopSeekDue,
  roomMembersLeftBehind,
  roomPositionAt,
  roomReloadInFlight,
  roomSeekAlreadyPending,
  roomReloadAllowed,
  roomReloadLanded,
  roomReloadMaxIntervalMs,
  roomReloadMaxLeadSeconds,
  roomReloadMinIntervalMs,
  roomReloadStaleMs,
  roomSyncNudgeRate,
  settleRoomReloads,
} from "./roomSyncCatchup";

function seekableRanges(ranges: Array<[number, number]>): TimeRanges {
  return {
    length: ranges.length,
    start: (index: number) => ranges[index]?.[0] ?? 0,
    end: (index: number) => ranges[index]?.[1] ?? 0,
  } as TimeRanges;
}

describe("isNativePositionInRanges", () => {
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
});

describe("roomPositionAt", () => {
  const reference = { positionSeconds: 100, executeAtServerMs: 10_000, playing: true };

  it("advances a playing reference from execute_at on the server clock", () => {
    // Local 8 s is server 10 s with a +2 s offset: execution has just happened.
    expect(roomPositionAt(reference, 8_000, 2_000)).toBe(100);
    expect(roomPositionAt(reference, 11_000, 2_000)).toBe(103);
    // Before execution the room has not moved past the command's position.
    expect(roomPositionAt(reference, 5_000, 2_000)).toBe(100);
  });

  it("holds a paused or waiting reference in place", () => {
    expect(roomPositionAt({ ...reference, playing: false }, 60_000, 0)).toBe(100);
  });
});

describe("roomSyncNudgeRate", () => {
  it.each([
    // Below the gain floor the minimum 1 % nudge applies.
    [0.1, 1.01],
    [-0.1, 0.99],
    // In between, |d| / 25, quantized to 0.005.
    [0.5, 1.02],
    [0.4, 1.015],
    [-0.6, 0.975],
    // The 4 % cap holds however far off the viewer is.
    [1, 1.04],
    [-30, 0.96],
  ])("plays %ss of drift at %sx", (drift, rate) => {
    expect(roomSyncNudgeRate(drift)).toBe(rate);
  });

  it("stays on 0.005 steps inside the 1-4 % band", () => {
    for (let drift = -6; drift <= 6; drift += 0.037) {
      const rate = roomSyncNudgeRate(drift);
      expect(Math.abs(rate - 1)).toBeGreaterThanOrEqual(0.01 - 1e-9);
      expect(Math.abs(rate - 1)).toBeLessThanOrEqual(0.04 + 1e-9);
      expect(Math.round(rate * 200)).toBeCloseTo(rate * 200, 9);
      expect(Math.sign(rate - 1)).toBe(Math.sign(drift));
    }
  });
});

describe("nextRoomNudgeRate", () => {
  it("starts a nudge only above the deadband", () => {
    expect(nextRoomNudgeRate(0.4, false)).toBe(1);
    expect(nextRoomNudgeRate(-0.4, false)).toBe(1);
    expect(nextRoomNudgeRate(0.45, false)).toBeGreaterThan(1);
    expect(nextRoomNudgeRate(-0.45, false)).toBeLessThan(1);
  });

  it("keeps a running nudge until the drift settles", () => {
    // Inside the deadband a nudge that is already running keeps going.
    expect(nextRoomNudgeRate(0.3, true)).toBe(1.01);
    expect(nextRoomNudgeRate(-0.2, true)).toBe(0.99);
    expect(nextRoomNudgeRate(0.1, true)).toBe(1);
    expect(nextRoomNudgeRate(-0.05, true)).toBe(1);
  });

  it("follows a drift that crosses the room position instead of overshooting", () => {
    expect(nextRoomNudgeRate(-0.2, true)).toBeLessThan(1);
  });

  it("never exceeds the cap for drift the server has to correct", () => {
    expect(nextRoomNudgeRate(12, false)).toBe(1.04);
    expect(nextRoomNudgeRate(-12, true)).toBe(0.96);
  });
});

describe("decideRoomCommand", () => {
  const base = {
    targetPositionSeconds: 100,
    localPositionSeconds: 100,
    localPlaying: true,
    rateCapable: true,
    targetLocallySeekable: true,
  };
  const at = (offset: number) => base.targetPositionSeconds - offset;

  it("always seeks to a room seek", () => {
    expect(decideRoomCommand({ ...base, action: "seek" })).toEqual({ kind: "seek" });
    expect(
      decideRoomCommand({
        ...base,
        action: "seek",
        localPlaying: false,
        targetLocallySeekable: false,
      }),
    ).toEqual({ kind: "seek" });
  });

  describe("play for a playing viewer", () => {
    it("leaves drift within the deadband alone", () => {
      expect(
        decideRoomCommand({ ...base, action: "play", localPositionSeconds: at(0.375) }),
      ).toEqual({
        kind: "none",
      });
    });

    it.each([true, false])(
      "nudges up to the hard-seek bound even when seekable is %s",
      (targetLocallySeekable) => {
        expect(
          decideRoomCommand({
            ...base,
            action: "play",
            targetLocallySeekable,
            localPositionSeconds: at(5),
          }),
        ).toEqual({ kind: "rate", rate: 1.04 });
        expect(
          decideRoomCommand({
            ...base,
            action: "play",
            targetLocallySeekable,
            localPositionSeconds: at(-0.5),
          }),
        ).toEqual({ kind: "rate", rate: 0.98 });
      },
    );

    it("seeks beyond the hard-seek bound", () => {
      expect(
        decideRoomCommand({
          ...base,
          action: "play",
          targetLocallySeekable: false,
          localPositionSeconds: at(5.1),
        }),
      ).toEqual({ kind: "seek" });
    });
  });

  describe.each(["play", "pause"] as const)("%s for a viewer that is not playing", (action) => {
    const stopped = { ...base, action, localPlaying: false };

    it("seeks a moderate drift only when the target is reachable in place", () => {
      expect(decideRoomCommand({ ...stopped, localPositionSeconds: at(1.1) })).toEqual({
        kind: "seek",
      });
      expect(
        decideRoomCommand({
          ...stopped,
          targetLocallySeekable: false,
          localPositionSeconds: at(-4.9),
        }),
      ).toEqual({ kind: "none" });
      expect(decideRoomCommand({ ...stopped, localPositionSeconds: at(1) })).toEqual({
        kind: "none",
      });
    });

    it("seeks beyond the hard-seek bound even through a rebuild", () => {
      expect(
        decideRoomCommand({
          ...stopped,
          targetLocallySeekable: false,
          localPositionSeconds: at(-5.1),
        }),
      ).toEqual({ kind: "seek" });
    });
  });

  it("never nudges a pause, even for a viewer still playing", () => {
    expect(decideRoomCommand({ ...base, action: "pause", localPositionSeconds: at(0.8) })).toEqual({
      kind: "none",
    });
  });
});

describe("decideRoomCommand without pitch-kept rate changes", () => {
  const playing = {
    action: "play" as const,
    targetPositionSeconds: 100,
    localPlaying: true,
    rateCapable: false,
    targetLocallySeekable: true,
  };

  it("never nudges", () => {
    expect(decideRoomCommand({ ...playing, localPositionSeconds: 99.5 })).toEqual({
      kind: "none",
    });
    expect(decideRoomCommand({ ...playing, localPositionSeconds: 99 })).toEqual({ kind: "none" });
  });

  it("seeks in place above one second", () => {
    expect(decideRoomCommand({ ...playing, localPositionSeconds: 101.1 })).toEqual({
      kind: "seek",
    });
  });

  it("loads media only beyond the hard-seek bound", () => {
    const unseekable = { ...playing, targetLocallySeekable: false };
    expect(decideRoomCommand({ ...unseekable, localPositionSeconds: 96 })).toEqual({
      kind: "none",
    });
    expect(decideRoomCommand({ ...unseekable, localPositionSeconds: 94.9 })).toEqual({
      kind: "seek",
    });
  });

  it("treats a stopped viewer like any other client", () => {
    expect(
      decideRoomCommand({
        ...playing,
        localPlaying: false,
        targetLocallySeekable: false,
        localPositionSeconds: 97,
      }),
    ).toEqual({ kind: "none" });
  });
});

describe("roomLoopSeekDue", () => {
  it("seeks only drift beyond one second that the element reaches in place", () => {
    expect(roomLoopSeekDue(1, true)).toBe(false);
    expect(roomLoopSeekDue(-1.2, true)).toBe(true);
    expect(roomLoopSeekDue(30, false)).toBe(false);
  });
});

describe("seek-based drift correction", () => {
  it("aims the next seek ahead by what the last one fell short", () => {
    const corrector = createRoomSeekCorrector();
    noteRoomCorrectorSeek(corrector, 0);
    observeRoomCorrector(corrector, 2);
    expect(roomCorrectorSeekTarget(corrector, 100)).toBe(102);
    // Landing past the room takes some of the lead back, never below none.
    noteRoomCorrectorSeek(corrector, 0);
    observeRoomCorrector(corrector, -0.5);
    expect(roomCorrectorSeekTarget(corrector, 100)).toBe(101.5);
    noteRoomCorrectorSeek(corrector, 0);
    observeRoomCorrector(corrector, -4);
    expect(corrector.leadSeconds).toBe(0);
  });

  it("caps the lead", () => {
    const corrector = createRoomSeekCorrector();
    for (let i = 0; i < 3; i++) {
      noteRoomCorrectorSeek(corrector, 0);
      observeRoomCorrector(corrector, 2);
    }
    expect(corrector.leadSeconds).toBe(3);
  });

  it("doubles the wait after each miss up to 16 s and resets once a seek converges", () => {
    const corrector = createRoomSeekCorrector();
    const waits: number[] = [];
    for (let i = 0; i < 6; i++) {
      noteRoomCorrectorSeek(corrector, 0);
      observeRoomCorrector(corrector, 1.5);
      waits.push(corrector.intervalMs);
    }
    expect(waits).toEqual([2_000, 4_000, 8_000, 16_000, 16_000, 16_000]);
    noteRoomCorrectorSeek(corrector, 0);
    observeRoomCorrector(corrector, 0.3);
    expect(corrector.intervalMs).toBe(1_000);
  });

  it("measures each seek once, and spaces seeks by the current wait", () => {
    const corrector = createRoomSeekCorrector();
    expect(roomCorrectorSeekDue(corrector, 1.5, true, 0)).toBe(true);
    expect(roomCorrectorSeekDue(corrector, 1.5, false, 0)).toBe(false);
    noteRoomCorrectorSeek(corrector, 10_000);
    observeRoomCorrector(corrector, 1.5);
    // Later ticks of the same landing are not new measurements.
    observeRoomCorrector(corrector, 1.5);
    expect(corrector.intervalMs).toBe(2_000);
    expect(corrector.leadSeconds).toBe(1.5);
    expect(roomCorrectorSeekDue(corrector, 1.5, true, 11_999)).toBe(false);
    expect(roomCorrectorSeekDue(corrector, 1.5, true, 12_000)).toBe(true);
  });
});

describe("roomSeekAlreadyPending", () => {
  const issued = { targetSeconds: 100, issuedAtMs: 1_000, commandId: "c1" };

  it("recognizes a repeat of a room seek that is still loading", () => {
    expect(roomSeekAlreadyPending(issued, 100, 100.8, 2_000)).toBe(true);
    expect(roomSeekAlreadyPending(issued, 100, 101.5, 2_000)).toBe(false);
  });

  it("does not mistake a landed, failed, or unrelated seek for it", () => {
    expect(roomSeekAlreadyPending(issued, null, 100, 2_000)).toBe(false);
    // A seek the viewer requested and the room has not commanded yet.
    expect(roomSeekAlreadyPending(null, 100, 100, 2_000)).toBe(false);
    expect(roomSeekAlreadyPending(issued, 250, 100, 2_000)).toBe(false);
    // Unfinished for as long as a stale reload: it is not coming.
    expect(roomSeekAlreadyPending(issued, 100, 100, 1_000 + roomReloadStaleMs)).toBe(false);
  });
});

describe("room rebuild budget", () => {
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

  it("counts a reload as in flight only until it goes stale", () => {
    const budget = createRoomReloadBudget();
    expect(roomReloadInFlight(budget, 0)).toBe(false);
    beginRoomReload(budget, 100, 1_000);
    expect(roomReloadInFlight(budget, 1_000 + roomReloadStaleMs - 1)).toBe(true);
    expect(roomReloadInFlight(budget, 1_000 + roomReloadStaleMs)).toBe(false);
  });

  it("lets a replacement start as soon as a stale reload expires", () => {
    const budget = createRoomReloadBudget();
    beginRoomReload(budget, 100, 0);
    noteRoomReloadLoading(budget);
    const generation = budget.generation;
    expireRoomReload(budget, roomReloadStaleMs);
    expect(budget.targetSeconds).toBeNull();
    expect(budget.generation).toBe(generation + 1);
    expect(roomReloadAllowed(budget, roomReloadStaleMs)).toBe(true);
    // The failed attempt still counts toward the next backoff.
    beginRoomReload(budget, 130, roomReloadStaleMs);
    landRoomReload(budget, roomReloadStaleMs);
    expect(budget.nextAllowedAtMs - roomReloadStaleMs).toBe(2 * roomReloadMinIntervalMs);
  });

  it("leaves a budget without a reload untouched on expiry", () => {
    const budget = createRoomReloadBudget();
    beginRoomReload(budget, 100, 0);
    abandonRoomReload(budget, 1_000);
    const before = { ...budget };
    expireRoomReload(budget, 2_000);
    expect(budget).toEqual(before);
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
