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

  it("keeps enough history for output latency at high sample rates", () => {
    // 192 kHz: an anchor every 4 quanta is every 2.67 ms.
    const step = (4 * 128) / 192_000;
    let audible = 0;
    const clock = new EngineClock(() => audible, 0);
    let t = 0;
    for (; t < 1; t += step) {
      clock.onAnchor({ contextTime: t, mediaTime: t * 3, advancing: true, rate: 3 });
    }
    clock.onAnchor({ contextTime: t, mediaTime: t * 3, advancing: true, rate: 0.5 });
    // 300 ms of output latency: what plays now was rendered at 3x.
    audible = t - 0.3;
    expect(clock.now()).toBeCloseTo(audible * 3, 6);
  });

  it("scales by the playback rate", () => {
    let audible = 0;
    const clock = new EngineClock(() => audible, 0);
    clock.setRate(1.25);
    clock.onAnchor({ contextTime: 0, mediaTime: 0, advancing: true });
    audible = 4;
    expect(clock.now()).toBeCloseTo(5, 6);
  });

  // The speakers lag the render by the output latency, so audio rendered at
  // the old rate is still playing for a moment after a speed change.
  it("keeps time across a rate change with output latency", () => {
    let audible = 9.8;
    const clock = new EngineClock(() => audible, 0);
    for (let t = 9; t <= 10; t += 0.1) {
      clock.onAnchor({ contextTime: t, mediaTime: 70 + (t - 9) * 3, advancing: true, rate: 3 });
    }
    const before = clock.now();
    expect(before).toBeCloseTo(72.4, 6);
    // The worklet switches to 0.5x at context time 10.
    clock.setRate(0.5);
    clock.onAnchor({ contextTime: 10.1, mediaTime: 73.05, advancing: true, rate: 0.5 });
    expect(clock.now()).toBeCloseTo(before, 6);
    // Audio from before the switch keeps playing at 3x until it is out.
    audible = 9.9;
    expect(clock.now()).toBeCloseTo(72.7, 6);
    audible = 10.2;
    expect(clock.now()).toBeCloseTo(73.1, 6);
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
