import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  markCriticalContentLoaded,
  resetCriticalContentLoadedForTests,
  STARTUP_DEFER_FALLBACK_MS,
  useCriticalContentLoaded,
} from "./startupPriority";

describe("startup priority", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    resetCriticalContentLoadedForTests();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("holds deferred widgets until the page marks its first content", () => {
    const { result } = renderHook(() => useCriticalContentLoaded());
    expect(result.current).toBe(false);

    act(() => markCriticalContentLoaded());
    expect(result.current).toBe(true);
  });

  it("releases them after the fallback when nothing marks the content", () => {
    const { result } = renderHook(() => useCriticalContentLoaded());

    act(() => vi.advanceTimersByTime(STARTUP_DEFER_FALLBACK_MS - 1));
    expect(result.current).toBe(false);
    act(() => vi.advanceTimersByTime(1));
    expect(result.current).toBe(true);
  });
});
