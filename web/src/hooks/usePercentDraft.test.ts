import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { parsePercent, usePercentDraft } from "./usePercentDraft";

describe("parsePercent", () => {
  it("clamps to min-100 and rounds", () => {
    expect(parsePercent("0", 1)).toBe(1);
    expect(parsePercent("0", 0)).toBe(0);
    expect(parsePercent("150", 1)).toBe(100);
    expect(parsePercent("49.6", 1)).toBe(50);
    expect(parsePercent(" 40% ", 1)).toBe(40);
  });

  it("rejects text that is not a number instead of saving the floor", () => {
    expect(parsePercent("", 1)).toBeNull();
    expect(parsePercent("   ", 1)).toBeNull();
    expect(parsePercent("abc", 1)).toBeNull();
  });
});

describe("usePercentDraft", () => {
  function setup(initial: number) {
    const onChange = vi.fn();
    const hook = renderHook(({ value }) => usePercentDraft(value, 1, onChange), {
      initialProps: { value: initial },
    });
    return { hook, onChange };
  }

  it("commits a changed value, clamped", () => {
    const { hook, onChange } = setup(100);
    act(() => hook.result.current.setDraft("0"));
    expect(hook.result.current.draft).toBe("0");
    act(() => hook.result.current.commit("0"));
    expect(onChange).toHaveBeenCalledWith(1);
  });

  it("does not save when focus leaves without a change", () => {
    const { hook, onChange } = setup(60);
    act(() => hook.result.current.commit("60"));
    act(() => hook.result.current.commit("999"));
    hook.rerender({ value: 100 });
    act(() => hook.result.current.commit("999"));
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(100);
  });

  it("reverts an emptied field to the current value", () => {
    const { hook, onChange } = setup(40);
    act(() => hook.result.current.setDraft(""));
    act(() => hook.result.current.commit(""));
    expect(onChange).not.toHaveBeenCalled();
    expect(hook.result.current.draft).toBe("40");
  });

  it("shows the real value after a commit, even if the save never landed", () => {
    const { hook } = setup(100);
    act(() => hook.result.current.setDraft("50"));
    act(() => hook.result.current.commit("50"));
    // The parent's save failed, so value stays 100: the field must say so.
    expect(hook.result.current.draft).toBe("100");
  });

  it("follows a value synced from elsewhere while not editing", () => {
    const { hook } = setup(100);
    hook.rerender({ value: 30 });
    expect(hook.result.current.draft).toBe("30");
  });
});
