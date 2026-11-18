import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { V2ProblemError } from "@/api/v2/request";
import { appearanceCache, storage } from "@/utils/storage";

import { useShowThenSave } from "./settingWriteOffline";

const toastError = vi.hoisted(() => vi.fn());
vi.mock("sonner", () => ({ toast: { error: toastError } }));

function problem(status: number) {
  return new V2ProblemError("op", {
    type: "https://siloserver.org/docs/api/v2/problems/validation_failed",
    title: "Rejected",
    status,
    detail: "Rejected",
  } as never);
}

const CACHE_KEY = storage.KEYS.UI_TEXT_SCALE;
const REJECTED = problem(422);

// A macrotask runs only after every queued settle handler has.
const settled = () => new Promise((resolve) => setTimeout(resolve, 0));

describe("useShowThenSave after an online failure", () => {
  beforeEach(() => {
    toastError.mockClear();
    appearanceCache.set(CACHE_KEY, "normal", null);
  });

  /** Makes each change in turn, then settles their saves in order: an Error rejects. */
  async function change(values: string[], outcomes: unknown[]) {
    const settles: Array<(outcome: unknown) => void> = [];
    const { result } = renderHook(() => useShowThenSave(null));
    let current = "normal";
    for (const value of values) {
      result.current({
        key: "ui.text_scale",
        cacheKey: CACHE_KEY,
        value,
        previous: current,
        show: (v: string) => {
          current = v;
        },
        save: () =>
          new Promise((resolve, reject) =>
            settles.push((outcome) => (outcome instanceof Error ? reject(outcome) : resolve(0))),
          ),
      });
    }
    for (const [i, outcome] of outcomes.entries()) {
      settles[i]!(outcome);
      await settled();
    }
    return { shown: current, cached: appearanceCache.get(CACHE_KEY, null) };
  }

  // A 4xx other than 408 means the server stored nothing.
  it.each([409, 422])("puts back the previous value after a %s rejection", async (status) => {
    expect(await change(["large"], [problem(status)])).toEqual({
      shown: "normal",
      cached: "normal",
    });
    expect(toastError).toHaveBeenCalledTimes(1);
  });

  it("puts back the value from before two rejected changes", async () => {
    expect(await change(["large", "x-large"], [REJECTED, REJECTED])).toEqual({
      shown: "normal",
      cached: "normal",
    });
  });

  it("puts back an earlier change that saved", async () => {
    expect(await change(["large", "x-large"], ["saved", REJECTED])).toEqual({
      shown: "large",
      cached: "large",
    });
  });

  // These may have saved; the effective-settings re-read decides what shows.
  it.each([
    ["500", problem(500)],
    ["408", problem(408)],
    ["network", new TypeError("Failed to fetch")],
  ])("keeps the change after an ambiguous %s failure", async (_label, error) => {
    expect(await change(["large"], [error])).toEqual({ shown: "large", cached: "large" });
    expect(toastError).not.toHaveBeenCalled();
  });
});
