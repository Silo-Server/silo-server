// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { shouldHeartbeat, useReadingHeartbeat } from "./useReadingHeartbeat";

function setHidden(hidden: boolean) {
  Object.defineProperty(document, "hidden", {
    value: hidden,
    configurable: true,
  });
}

describe("shouldHeartbeat", () => {
  it("is true when visible and activity is fresh", () => {
    expect(shouldHeartbeat({ visible: true, lastActivityAt: 1_000, now: 1_000 })).toBe(true);
    expect(shouldHeartbeat({ visible: true, lastActivityAt: 0, now: 60_000 })).toBe(true);
  });

  it("is false when hidden even if activity is fresh", () => {
    expect(shouldHeartbeat({ visible: false, lastActivityAt: 1_000, now: 1_000 })).toBe(false);
  });

  it("is false when activity is stale (>60s)", () => {
    expect(shouldHeartbeat({ visible: true, lastActivityAt: 0, now: 60_001 })).toBe(false);
  });
});

describe("useReadingHeartbeat", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(0);
    setHidden(false);
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("sends immediately on first activity, then every 30s while active", () => {
    const send = vi.fn();
    const { result } = renderHook(() =>
      useReadingHeartbeat({ contentId: "book-1", getFraction: () => 0.5, send }),
    );

    act(() => result.current.noteActivity());
    expect(send).toHaveBeenCalledTimes(1);
    expect(send).toHaveBeenLastCalledWith("book-1", 0.5);

    act(() => vi.advanceTimersByTime(30_000));
    expect(send).toHaveBeenCalledTimes(2);

    act(() => vi.advanceTimersByTime(30_000));
    expect(send).toHaveBeenCalledTimes(3);
  });

  it("stops when idle or hidden and resumes on activity", () => {
    const send = vi.fn();
    const { result } = renderHook(() =>
      useReadingHeartbeat({ contentId: "book-1", getFraction: () => 0.2, send }),
    );

    act(() => result.current.noteActivity());
    expect(send).toHaveBeenCalledTimes(1);

    // No further activity: the 60s freshness window lapses somewhere in
    // here, and once it does the interval must stop ticking on its own.
    act(() => vi.advanceTimersByTime(90_000));
    const callsAfterIdle = send.mock.calls.length;

    // Idle for even longer: no additional sends once stale.
    act(() => vi.advanceTimersByTime(60_000));
    expect(send).toHaveBeenCalledTimes(callsAfterIdle);

    // Fresh activity resumes immediately, without waiting for a tick.
    act(() => result.current.noteActivity());
    expect(send).toHaveBeenCalledTimes(callsAfterIdle + 1);

    // Hidden: activity no longer produces sends, and any running interval
    // stops.
    send.mockClear();
    setHidden(true);
    act(() => document.dispatchEvent(new Event("visibilitychange")));
    act(() => result.current.noteActivity());
    act(() => vi.advanceTimersByTime(60_000));
    expect(send).not.toHaveBeenCalled();

    setHidden(false);
  });

  it("does nothing when contentId is null", () => {
    const send = vi.fn();
    const { result } = renderHook(() =>
      useReadingHeartbeat({ contentId: null, getFraction: () => 0.9, send }),
    );

    act(() => result.current.noteActivity());
    act(() => vi.advanceTimersByTime(120_000));

    expect(send).not.toHaveBeenCalled();
  });
});
