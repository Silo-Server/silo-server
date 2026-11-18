// @vitest-environment node

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  STARTUP_TIMEOUT_MS,
  StartupGuard,
  isStaleGenerationError,
  recoverFromStaleGeneration,
} from "./startupGuard";

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

  it("fails at the first deadline when replanning adopts transports before a frame", () => {
    const onFailure = vi.fn();
    const guard = new StartupGuard(onFailure);

    // Two plans adopted inside the window, each installing a new transport. A
    // start that never showed a frame must not push its deadline out with them.
    vi.advanceTimersByTime(20_000);
    guard.armForReplacementTransport();
    vi.advanceTimersByTime(20_000);
    guard.armForReplacementTransport();

    vi.advanceTimersByTime(STARTUP_TIMEOUT_MS - 40_000 - 1);
    expect(onFailure).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(onFailure).toHaveBeenCalledExactlyOnceWith("deadline");
    expect(guard.hasFailed()).toBe(true);
    guard.dispose();
  });

  it("gives a transport that replaces a played one its own startup window", () => {
    const onFailure = vi.fn();
    const guard = new StartupGuard(onFailure);

    guard.markPlaybackStarted();
    vi.advanceTimersByTime(STARTUP_TIMEOUT_MS);
    expect(onFailure).not.toHaveBeenCalled();

    // A real reconnect from a working stream is a new start and may use the
    // full window.
    guard.armForReplacementTransport();
    expect(guard.isStarting()).toBe(true);
    vi.advanceTimersByTime(STARTUP_TIMEOUT_MS - 1);
    expect(onFailure).not.toHaveBeenCalled();

    vi.advanceTimersByTime(1);
    expect(onFailure).toHaveBeenCalledExactlyOnceWith("deadline");
    guard.dispose();
  });

  it("re-arms for the retry a failed start lowered into", () => {
    const onFailure = vi.fn();
    const guard = new StartupGuard(onFailure);

    vi.advanceTimersByTime(STARTUP_TIMEOUT_MS);
    expect(onFailure).toHaveBeenCalledExactlyOnceWith("deadline");

    // The reported failure is the Auto ladder's cue to lower; its replacement
    // transport is a genuinely new attempt with its own deadline.
    guard.armForReplacementTransport();
    expect(guard.hasFailed()).toBe(false);
    expect(guard.isStarting()).toBe(true);

    vi.advanceTimersByTime(STARTUP_TIMEOUT_MS);
    expect(onFailure).toHaveBeenCalledTimes(2);
    guard.dispose();
  });

  it("does not revive a disposed guard for a replacement transport", () => {
    const onFailure = vi.fn();
    const guard = new StartupGuard(onFailure);

    guard.dispose();
    guard.armForReplacementTransport();
    vi.advanceTimersByTime(STARTUP_TIMEOUT_MS);

    expect(onFailure).not.toHaveBeenCalled();
  });
});

describe("isStaleGenerationError", () => {
  it("detects the server's 412 stale-generation fence on a fetch response", () => {
    expect(isStaleGenerationError({ response: { code: 412 } })).toBe(true);
  });

  it("detects the fence from the xhr network detail when the loader response omits it", () => {
    expect(isStaleGenerationError({ networkDetails: { status: 412 } })).toBe(true);
  });

  it("does not treat another status as the fence", () => {
    for (const code of [200, 404, 422, 500, 503]) {
      expect(isStaleGenerationError({ response: { code } })).toBe(false);
    }
    expect(isStaleGenerationError({ networkDetails: { status: 404 } })).toBe(false);
  });

  it("ignores an unreadable or absent carrier", () => {
    expect(isStaleGenerationError(null)).toBe(false);
    expect(isStaleGenerationError(undefined)).toBe(false);
    expect(isStaleGenerationError({})).toBe(false);
  });
});

describe("recoverFromStaleGeneration", () => {
  it("refetches the cached playlist instead of retrying the dead segment URL", () => {
    const host = {
      url: "/api/v2/playback/transcode/session-1/master.m3u8?st=jwt",
      config: {} as { startPosition?: number },
      loadSource: vi.fn(),
    };

    expect(recoverFromStaleGeneration(host, 42.5)).toBe(true);
    expect(host.loadSource).toHaveBeenCalledOnce();
    // The same playlist URL is reloaded, so the server re-mints every segment
    // URI with the current sgen; the dead URL is never re-requested directly.
    expect(host.loadSource).toHaveBeenCalledWith(host.url);
    // The refetch resumes where the viewer is, rather than jumping to the
    // generation origin.
    expect(host.config.startPosition).toBe(42.5);
  });

  it("omits the resume position when the playhead is not usable", () => {
    const host = {
      url: "/master.m3u8",
      config: {} as { startPosition?: number },
      loadSource: vi.fn(),
    };

    expect(recoverFromStaleGeneration(host, 0)).toBe(true);
    expect(host.config.startPosition).toBeUndefined();
    expect(host.loadSource).toHaveBeenCalledWith("/master.m3u8");
  });

  it("reports false when no playlist URL was loaded", () => {
    const host = {
      url: null,
      config: {} as { startPosition?: number },
      loadSource: vi.fn(),
    };

    expect(recoverFromStaleGeneration(host, 12)).toBe(false);
    expect(host.loadSource).not.toHaveBeenCalled();
  });
});
