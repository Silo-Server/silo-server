import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useSubtitleDelay } from "./useSubtitleDelay";

const sync = vi.hoisted(() => ({ offsetMs: 0, isLoaded: true, save: vi.fn() }));
vi.mock("@/hooks/queries/subtitleSync", async () => {
  const actual = await vi.importActual<typeof import("@/hooks/queries/subtitleSync")>(
    "@/hooks/queries/subtitleSync",
  );
  return {
    ...actual,
    useSubtitleSyncSetting: () => ({
      offsetMs: sync.offsetMs,
      isLoaded: sync.isLoaded,
      save: sync.save,
    }),
  };
});

describe("useSubtitleDelay", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    sync.offsetMs = 200;
    sync.isLoaded = true;
    sync.save.mockReset().mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("starts from the profile's saved offset", () => {
    const { result } = renderHook(() => useSubtitleDelay());
    expect(result.current[0]).toBe(200);
  });

  it("shows each nudge at once and saves only the last one after a pause", async () => {
    const { result } = renderHook(() => useSubtitleDelay());

    act(() => result.current[1](300));
    act(() => result.current[1](400));
    expect(result.current[0]).toBe(400);
    expect(sync.save).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(600);
      await Promise.resolve();
    });
    expect(sync.save).toHaveBeenCalledTimes(1);
    expect(sync.save).toHaveBeenCalledWith(400);
  });

  it("clamps to the contract's bounds", () => {
    const { result } = renderHook(() => useSubtitleDelay());
    act(() => result.current[1](-12_000));
    expect(result.current[0]).toBe(-10_000);
  });

  it("saves a nudge that is still waiting when the player closes", async () => {
    const { result, unmount } = renderHook(() => useSubtitleDelay());
    act(() => result.current[1](500));

    unmount();
    await Promise.resolve();

    expect(sync.save).toHaveBeenCalledWith(500);
  });

  it("keeps the nudged value for this session when the save fails", async () => {
    sync.save.mockRejectedValue(new Error("offline"));
    const { result } = renderHook(() => useSubtitleDelay());

    act(() => result.current[1](700));
    await act(async () => {
      vi.advanceTimersByTime(600);
      await Promise.resolve();
    });

    expect(result.current[0]).toBe(700);
  });

  it("ignores nudges until the saved offset has loaded", () => {
    sync.isLoaded = false;
    sync.offsetMs = 0;
    const { result, unmount } = renderHook(() => useSubtitleDelay());

    act(() => result.current[1](100));
    act(() => vi.advanceTimersByTime(600));
    unmount();

    expect(result.current[0]).toBe(0);
    expect(sync.save).not.toHaveBeenCalled();
  });

  it("sends a later save only after the earlier one has finished", async () => {
    let finishFirst!: () => void;
    sync.save.mockImplementationOnce(() => new Promise<void>((resolve) => (finishFirst = resolve)));
    const { result } = renderHook(() => useSubtitleDelay());

    act(() => result.current[1](300));
    act(() => vi.advanceTimersByTime(600));
    act(() => result.current[1](400));
    await act(async () => {
      vi.advanceTimersByTime(600);
      await Promise.resolve();
    });
    expect(sync.save).toHaveBeenCalledTimes(1);

    await act(async () => {
      finishFirst();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(sync.save).toHaveBeenCalledTimes(2);
    expect(sync.save).toHaveBeenLastCalledWith(400);
  });

  it("keeps showing the nudged value until the saved offset reflects it", async () => {
    const { result, rerender } = renderHook(() => useSubtitleDelay());

    act(() => result.current[1](300));
    await act(async () => {
      vi.advanceTimersByTime(600);
      await Promise.resolve();
    });
    // The save went through but the refetch has not shown it yet.
    expect(result.current[0]).toBe(300);

    sync.offsetMs = 300;
    rerender();
    sync.offsetMs = 900;
    rerender();
    expect(result.current[0]).toBe(900);
  });

  it("keeps a nudge back to the saved value until its queued save lands", async () => {
    let finishFirst!: () => void;
    sync.save
      .mockImplementationOnce(() => new Promise<void>((resolve) => (finishFirst = resolve)))
      .mockRejectedValueOnce(new Error("offline"));
    const { result, rerender } = renderHook(() => useSubtitleDelay());

    act(() => result.current[1](300));
    act(() => vi.advanceTimersByTime(600));
    act(() => result.current[1](200));
    expect(result.current[0]).toBe(200);
    await act(async () => {
      vi.advanceTimersByTime(600);
      await Promise.resolve();
    });

    // The first save lands and the setting refetches 300; the queued 200 then fails.
    sync.offsetMs = 300;
    await act(async () => {
      finishFirst();
      for (let i = 0; i < 6; i++) await Promise.resolve();
    });
    rerender();

    expect(sync.save).toHaveBeenCalledTimes(2);
    expect(sync.save).toHaveBeenLastCalledWith(200);
    expect(result.current[0]).toBe(200);
  });
});
