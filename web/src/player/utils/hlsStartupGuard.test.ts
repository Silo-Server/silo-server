import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { HLS_STARTUP_TIMEOUT_MS, HlsStartupGuard } from "./hlsStartupGuard";

describe("HlsStartupGuard", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("fails startup when no playable frame arrives before the deadline", () => {
    const onFailure = vi.fn();
    const guard = new HlsStartupGuard(onFailure);

    vi.advanceTimersByTime(HLS_STARTUP_TIMEOUT_MS - 1);
    expect(onFailure).not.toHaveBeenCalled();
    expect(guard.hasFailed()).toBe(false);

    vi.advanceTimersByTime(1);
    expect(onFailure).toHaveBeenCalledOnce();
    expect(guard.hasFailed()).toBe(true);
    guard.dispose();
  });

  it("allows one fatal network recovery before failing startup", () => {
    const onFailure = vi.fn();
    const guard = new HlsStartupGuard(onFailure);

    expect(guard.handleFatalNetworkError()).toBe(true);
    expect(onFailure).not.toHaveBeenCalled();

    expect(guard.handleFatalNetworkError()).toBe(false);
    expect(onFailure).toHaveBeenCalledOnce();
    expect(guard.hasFailed()).toBe(true);
  });

  it("disarms startup limits after playable media arrives", () => {
    const onFailure = vi.fn();
    const guard = new HlsStartupGuard(onFailure);

    guard.markPlaybackStarted();
    vi.advanceTimersByTime(HLS_STARTUP_TIMEOUT_MS);

    expect(onFailure).not.toHaveBeenCalled();
    expect(guard.hasFailed()).toBe(false);
    expect(guard.handleFatalNetworkError()).toBe(true);
    expect(guard.handleFatalNetworkError()).toBe(true);
  });

  it("does not fail after disposal", () => {
    const onFailure = vi.fn();
    const guard = new HlsStartupGuard(onFailure);

    guard.dispose();
    vi.advanceTimersByTime(HLS_STARTUP_TIMEOUT_MS);

    expect(onFailure).not.toHaveBeenCalled();
  });
});
