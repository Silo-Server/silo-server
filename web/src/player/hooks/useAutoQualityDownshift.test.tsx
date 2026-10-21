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

  it("counts a startup stall from the end of the grace period", () => {
    const tracker = createStallTracker();
    tracker.playing(0);
    tracker.waiting(5_000);
    expect(tracker.shouldDownshift(15_000)).toBe(false);
    expect(tracker.shouldDownshift(16_000)).toBe(true);
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

  it("still counts a long stall that ended before it was checked", () => {
    const tracker = createStallTracker();
    tracker.playing(0);
    tracker.waiting(20_000);
    tracker.playing(27_000);
    expect(tracker.shouldDownshift(27_000)).toBe(true);
  });

  it("times a start that never produces a frame from its first wait", () => {
    const tracker = createStallTracker();
    tracker.waiting(1_000);
    expect(tracker.shouldDownshift(16_000)).toBe(false);
    expect(tracker.shouldDownshift(17_000)).toBe(true);
  });

  it("forgets a finished long stall after the stall window", () => {
    const tracker = createStallTracker();
    tracker.playing(0);
    tracker.waiting(20_000);
    tracker.playing(27_000);
    expect(tracker.shouldDownshift(27_000 + 2 * 60_000 + 1)).toBe(false);
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
      currentTime: number;
    };
    video.currentTime = 0;
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

  it("treats advancing time as the end of a stall", () => {
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
    video.currentTime = 30;
    fire(video, "waiting", 30_000);
    // Recovered without a `playing` event.
    video.currentTime = 30.5;
    fire(video, "timeupdate", 30_400);
    vi.advanceTimersByTime(10_000);
    expect(onDownshift).not.toHaveBeenCalled();
  });

  function renderOnAuto(video: EventTarget, onDownshift: () => void) {
    return renderHook(() =>
      useAutoQualityDownshift({
        videoRef: { current: video as unknown as HTMLVideoElement },
        enabled: true,
        sessionId: "s1",
        planIdentity: "plan-1",
        deliveredKbps: 80_000,
        onDownshift,
      }),
    );
  }

  it("does not count a stall the viewer paused", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    renderOnAuto(video, onDownshift);
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    video.paused = true;
    fire(video, "pause", 31_000);
    vi.advanceTimersByTime(10_000);
    expect(onDownshift).not.toHaveBeenCalled();
  });

  it("does not downshift a paused element when the timer fires", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    renderOnAuto(video, onDownshift);
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    video.paused = true;
    vi.advanceTimersByTime(10_000);
    expect(onDownshift).not.toHaveBeenCalled();
  });

  it("tries again when the replan was not adopted", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    renderOnAuto(video, onDownshift);
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);

    // Same plan still playing a minute later: the replan failed.
    fire(video, "playing", 40_000);
    vi.advanceTimersByTime(60_000);
    fire(video, "waiting", 100_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(2);
  });

  it("keeps watching a stall that outlasts an unadopted replan", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    renderOnAuto(video, onDownshift);
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);

    // The replan failed and the element never recovered or fired again.
    vi.advanceTimersByTime(60_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(2);
  });

  it("leaves no stall timer behind when the re-arm restarts a stall", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    const { unmount } = renderOnAuto(video, onDownshift);
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);

    // A new stall begins just before the re-arm, then Auto is turned off.
    vi.advanceTimersByTime(57_000);
    fire(video, "waiting", 93_000);
    vi.advanceTimersByTime(3_000);
    unmount();
    vi.advanceTimersByTime(20_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);
  });

  it("downshifts a stall that began in startup and never recovered", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    renderOnAuto(video, onDownshift);
    fire(video, "playing", 0);
    fire(video, "waiting", 5_000);
    vi.advanceTimersByTime(10_000);
    expect(onDownshift).not.toHaveBeenCalled();
    vi.advanceTimersByTime(2_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);
  });

  it("retries an unadopted downshift after the hook was briefly disabled", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    const { rerender } = renderHook(
      ({ enabled }: { enabled: boolean }) =>
        useAutoQualityDownshift({
          videoRef: { current: video as unknown as HTMLVideoElement },
          enabled,
          sessionId: "s1",
          planIdentity: 1,
          deliveredKbps: 80_000,
          onDownshift,
        }),
      { initialProps: { enabled: true } },
    );
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);

    // A reconnect disables the hook before the re-arm; the same plan resumes.
    rerender({ enabled: false });
    rerender({ enabled: true });
    fire(video, "playing", 40_000);
    fire(video, "waiting", 60_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(2);
  });

  it("watches a plan an unrelated replan raised after a successful step down", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    const { rerender } = renderHook(
      ({ plan, kbps }: { plan: number; kbps: number }) =>
        useAutoQualityDownshift({
          videoRef: { current: video as unknown as HTMLVideoElement },
          enabled: true,
          sessionId: "s1",
          planIdentity: plan,
          deliveredKbps: kbps,
          onDownshift,
        }),
      { initialProps: { plan: 1, kbps: 80_000 } },
    );
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);

    rerender({ plan: 2, kbps: 20_000 }); // the step down worked
    rerender({ plan: 3, kbps: 80_000 }); // an audio change raised it again later
    fire(video, "playing", 100_000);
    fire(video, "waiting", 130_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(2);
  });

  it("does not read buffering during a transport failure as a slow link", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    let failing = true;
    renderHook(() =>
      useAutoQualityDownshift({
        videoRef: { current: video as unknown as HTMLVideoElement },
        enabled: true,
        sessionId: "s1",
        planIdentity: "plan-1",
        deliveredKbps: 80_000,
        onDownshift,
        transportTrouble: () => failing,
      }),
    );
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(10_000);
    expect(onDownshift).not.toHaveBeenCalled();

    // Once requests succeed again, a new stall counts as usual.
    failing = false;
    fire(video, "playing", 45_000);
    fire(video, "waiting", 60_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);
  });

  it("watches a no-lower plan again after the cooldown", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    const { rerender } = renderHook(
      ({ plan }: { plan: string }) =>
        useAutoQualityDownshift({
          videoRef: { current: video as unknown as HTMLVideoElement },
          enabled: true,
          sessionId: "s1",
          planIdentity: plan,
          deliveredKbps: 6_000,
          onDownshift,
        }),
      { initialProps: { plan: "plan-1" } },
    );
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);

    rerender({ plan: "plan-2" }); // came back no lower
    fire(video, "playing", 40_000);
    fire(video, "waiting", 60_000);
    vi.advanceTimersByTime(10_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);

    fire(video, "playing", 80_000);
    vi.setSystemTime(400_000);
    fire(video, "waiting", 400_000);
    vi.advanceTimersByTime(6_000);
    expect(onDownshift).toHaveBeenCalledTimes(2);
  });

  it("times a stall that outlasts an outage from when the outage clears", () => {
    const video = fakeVideo();
    const onDownshift = vi.fn();
    let failing = true;
    renderHook(() =>
      useAutoQualityDownshift({
        videoRef: { current: video as unknown as HTMLVideoElement },
        enabled: true,
        sessionId: "s1",
        planIdentity: "plan-1",
        deliveredKbps: 80_000,
        onDownshift,
        transportTrouble: () => failing,
      }),
    );
    fire(video, "playing", 0);
    fire(video, "waiting", 30_000);
    vi.advanceTimersByTime(8_000);
    expect(onDownshift).not.toHaveBeenCalled();

    failing = false; // requests succeed again, but the stall goes on
    vi.advanceTimersByTime(5_000);
    expect(onDownshift).not.toHaveBeenCalled();
    vi.advanceTimersByTime(2_000);
    expect(onDownshift).toHaveBeenCalledTimes(1);
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
