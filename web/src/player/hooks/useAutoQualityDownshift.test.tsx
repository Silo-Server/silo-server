import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  createStallTracker,
  downshiftEstimateKbps,
  useAutoQualityDownshift,
} from "./useAutoQualityDownshift";

describe("createStallTracker", () => {
  it("ignores buffering during startup", () => {
    const tracker = createStallTracker();
    tracker.playing(0);
    tracker.waiting(2_000);
    tracker.playing(5_000);
    tracker.waiting(6_000);
    tracker.playing(9_000);
    expect(tracker.shouldDownshift(9_000)).toBe(false);
  });

  it("downshifts after repeated stalls inside the window", () => {
    const tracker = createStallTracker();
    tracker.playing(0);
    tracker.waiting(20_000);
    tracker.playing(22_000);
    expect(tracker.shouldDownshift(22_000)).toBe(false);
    tracker.waiting(60_000);
    tracker.playing(62_000);
    expect(tracker.shouldDownshift(62_000)).toBe(true);
  });

  it("forgets stalls that fell out of the window", () => {
    const tracker = createStallTracker();
    tracker.playing(0);
    tracker.waiting(20_000);
    tracker.playing(22_000);
    tracker.waiting(200_000);
    tracker.playing(202_000);
    expect(tracker.shouldDownshift(202_000)).toBe(false);
  });

  it("does not count brief segment-boundary buffering", () => {
    const tracker = createStallTracker();
    tracker.playing(0);
    for (let at = 20_000; at < 60_000; at += 10_000) {
      tracker.waiting(at);
      tracker.playing(at + 400);
    }
    expect(tracker.shouldDownshift(60_000)).toBe(false);
  });

  it("downshifts on one long stall", () => {
    const tracker = createStallTracker();
    tracker.playing(0);
    tracker.waiting(20_000);
    expect(tracker.shouldDownshift(25_000)).toBe(false);
    expect(tracker.shouldDownshift(26_000)).toBe(true);
  });

  it("does not blame the network for a seek", () => {
    const tracker = createStallTracker();
    tracker.playing(0);
    tracker.waiting(20_000);
    tracker.seeking();
    expect(tracker.shouldDownshift(30_000)).toBe(false);
  });
});

describe("downshiftEstimateKbps", () => {
  it("plans below the bitrate that stalled", () => {
    expect(downshiftEstimateKbps(80_000)).toBe(56_000);
    expect(downshiftEstimateKbps(6_000)).toBe(4_200);
  });

  it("needs a known bitrate", () => {
    expect(downshiftEstimateKbps(undefined)).toBeUndefined();
    expect(downshiftEstimateKbps(0)).toBeUndefined();
    expect(downshiftEstimateKbps(100)).toBeUndefined();
  });
});

describe("useAutoQualityDownshift", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  function fakeVideo() {
    const video = new EventTarget() as EventTarget & {
      seeking: boolean;
      paused: boolean;
      readyState: number;
    };
    video.seeking = false;
    video.paused = false;
    video.readyState = 0;
    return video;
  }

  function fire(video: EventTarget, type: string, atMs: number) {
    vi.setSystemTime(atMs);
    video.dispatchEvent(new Event(type));
  }

  it("asks for a lower estimate after a long stall on Auto", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    renderHook(() =>
      useAutoQualityDownshift({
        videoRef: { current: video as unknown as HTMLVideoElement },
        enabled: true,
        sessionId: "s1",
        planIdentity: "plan-1",
        deliveredKbps: 80_000,
        onDownshift,
      }),
    );
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledExactlyOnceWith(56_000);
  });

  it("leaves an explicit quality alone", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    renderHook(() =>
      useAutoQualityDownshift({
        videoRef: { current: video as unknown as HTMLVideoElement },
        enabled: false,
        sessionId: "s1",
        planIdentity: "plan-1",
        deliveredKbps: 80_000,
        onDownshift,
      }),
    );
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(10_000);
    expect(onDownshift).not.toHaveBeenCalled();
  });

  it("stops when the replanned stream is no lower", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    const { rerender } = renderHook(
      ({ plan }: { plan: string }) =>
        useAutoQualityDownshift({
          videoRef: { current: video as unknown as HTMLVideoElement },
          enabled: true,
          sessionId: "s1",
          planIdentity: plan,
          deliveredKbps: 1_500,
          onDownshift,
        }),
      { initialProps: { plan: "plan-1" } },
    );
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);

    rerender({ plan: "plan-2" });
    fire(video, "playing", 40_000);
    fire(video, "waiting", 60_000);
    vi.advanceTimersByTime(10_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);
  });
});
