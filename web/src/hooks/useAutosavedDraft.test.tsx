// @vitest-environment jsdom

import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useAutosavedDraft } from "./useAutosavedDraft";

afterEach(cleanup);

describe("useAutosavedDraft", () => {
  it("sends an edit made while a save is in flight only after that save settles", async () => {
    let releaseFirst!: () => void;
    const firstSave = vi.fn(
      () => new Promise<void>((resolve) => { releaseFirst = resolve; }),
    );
    const secondSave = vi.fn(() => Promise.resolve());
    const { result } = renderHook(() => useAutosavedDraft({ a: 1 }));

    act(() => {
      result.current.edit({ a: 2 }, firstSave);
    });
    act(() => {
      result.current.edit({ a: 3 }, secondSave);
    });
    expect(firstSave).toHaveBeenCalledTimes(1);
    expect(secondSave).not.toHaveBeenCalled();

    await act(async () => {
      releaseFirst();
    });
    expect(secondSave).toHaveBeenCalledTimes(1);
  });

  it("whenIdle stays pending while a save is in flight, so a reset after it can't be undone", async () => {
    let releaseSave!: () => void;
    const save = vi.fn(
      () => new Promise<void>((resolve) => { releaseSave = resolve; }),
    );
    const onReset = vi.fn();
    const { result } = renderHook(() => useAutosavedDraft({ a: 1 }));

    act(() => {
      result.current.edit({ a: 2 }, save);
    });
    expect(result.current.saving).toBe(true);

    // The reset flow: drop queued edits, then remove the stored value only
    // once the save already on the wire has settled.
    act(() => {
      result.current.discardQueued();
      void result.current.whenIdle().then(() => onReset());
    });
    expect(onReset).not.toHaveBeenCalled();

    await act(async () => {
      releaseSave();
    });
    expect(onReset).toHaveBeenCalledTimes(1);
  });

  it("whenIdle resolves at once when nothing is in flight", async () => {
    const { result } = renderHook(() => useAutosavedDraft({ a: 1 }));
    expect(result.current.saving).toBe(false);

    const onReset = vi.fn();
    await act(async () => {
      await result.current.whenIdle();
      onReset();
    });
    expect(onReset).toHaveBeenCalledTimes(1);
  });
});
