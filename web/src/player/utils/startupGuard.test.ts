// @vitest-environment node

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { STARTUP_TIMEOUT_MS, StartupGuard } from "./startupGuard";

describe("StartupGuard", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("fails startup when no playable frame arrives before the deadline", () => {
    const onFailure = vi.fn();
    const guard = new StartupGuard(onFailure);

    vi.advanceTimersByTime(STARTUP_TIMEOUT_MS - 1);
    expect(onFailure).not.toHaveBeenCalled();
    expect(guard.hasFailed()).toBe(false);

    vi.advanceTimersByTime(1);
    expect(onFailure).toHaveBeenCalledExactlyOnceWith("deadline");
    expect(guard.hasFailed()).toBe(true);
    guard.dispose();
  });

  it("disarms startup limits after playable media arrives", () => {
    const onFailure = vi.fn();
    const guard = new StartupGuard(onFailure);

    guard.markPlaybackStarted();
    vi.advanceTimersByTime(STARTUP_TIMEOUT_MS);

    expect(onFailure).not.toHaveBeenCalled();
    expect(guard.hasFailed()).toBe(false);
    expect(guard.handleFatalNetworkError()).toBe(true);
    expect(guard.handleFatalNetworkError()).toBe(true);
  });

  it("does not fail after disposal", () => {
    const onFailure = vi.fn();
    const guard = new StartupGuard(onFailure);

    guard.dispose();
    vi.advanceTimersByTime(STARTUP_TIMEOUT_MS);

    expect(onFailure).not.toHaveBeenCalled();
  });
});
