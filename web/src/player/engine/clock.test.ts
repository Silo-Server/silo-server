import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EngineClock } from "./clock";

describe("EngineClock with audio", () => {
  it("reads the anchor at the audible context time", () => {
    let audible = 10;
    const clock = new EngineClock(() => audible, 30);
    expect(clock.now()).toBe(30);
    clock.onAnchor({ contextTime: 10, mediaTime: 30, advancing: true });
    audible = 10.5;
    expect(clock.now()).toBeCloseTo(30.5, 6);
  });

  it("holds at the run start until it becomes audible", () => {
    let audible = 9.9;
    const clock = new EngineClock(() => audible, 30);
    clock.onAnchor({ contextTime: 10, mediaTime: 30, advancing: true });
    expect(clock.now()).toBe(30);
    audible = 10.25;
    expect(clock.now()).toBeCloseTo(30.25, 6);
  });

  it("freezes on a non-advancing anchor and never steps backwards while running", () => {
    let audible = 20;
    const clock = new EngineClock(() => audible, 0);
    clock.onAnchor({ contextTime: 19, mediaTime: 5, advancing: true });
    expect(clock.now()).toBeCloseTo(6, 6);
    // A later anchor with jitter that would read slightly earlier.
    clock.onAnchor({ contextTime: 19.5, mediaTime: 5.49, advancing: true });
    expect(clock.now()).toBeCloseTo(6, 6);
    clock.onAnchor({ contextTime: 20, mediaTime: 6.2, advancing: false });
    audible = 30;
    expect(clock.now()).toBe(6.2);
  });

  it("scales by the playback rate", () => {
    let audible = 0;
    const clock = new EngineClock(() => audible, 0);
    clock.setRate(1.25);
    clock.onAnchor({ contextTime: 0, mediaTime: 0, advancing: true });
    audible = 4;
    expect(clock.now()).toBeCloseTo(5, 6);
  });
});

describe("EngineClock without audio", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("runs from wall time only while running", () => {
    const clock = new EngineClock(null, 12);
    vi.advanceTimersByTime(1_000);
    expect(clock.now()).toBe(12);
    clock.setRunning(true);
    vi.advanceTimersByTime(2_000);
    expect(clock.now()).toBeCloseTo(14, 2);
    clock.setRunning(false);
    vi.advanceTimersByTime(2_000);
    expect(clock.now()).toBeCloseTo(14, 2);
    clock.reset(100);
    expect(clock.now()).toBe(100);
  });
});
