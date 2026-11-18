import { renderHook } from "@testing-library/react";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { V2ProblemError } from "@/api/v2/request";
import { appearanceCache, storage } from "@/utils/storage";

import { useShowThenSave } from "./settingWriteOffline";

// A spy on the real toast, not a module mock: in a shared vmThreads worker
// another file may load sonner first, and the mock then misses this module.
const toastError = vi.spyOn(toast, "error").mockImplementation(() => 0);

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

  /**
   * Runs the steps in order: a string picks that value; [i, outcome] settles
   * the i-th pick's save with outcome (an Error rejects). Returns what is shown
   * and cached at the end.
   */
  async function run(steps: Array<string | [number, unknown]>) {
    const settles: Array<(outcome: unknown) => void> = [];
    const { result } = renderHook(() => useShowThenSave(null));
    let current = "normal";
    for (const step of steps) {
      if (typeof step !== "string") {
        const [i, outcome] = step;
        settles[i]!(outcome);
        await settled();
        continue;
      }
      result.current({
        key: "ui.text_scale",
        cacheKey: CACHE_KEY,
        value: step,
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
    return { shown: current, cached: appearanceCache.get(CACHE_KEY, null) };
  }

  // A 4xx other than 408 means the server stored nothing.
  it.each([409, 422])("puts back the previous value after a %s rejection", async (status) => {
    expect(await run(["large", [0, problem(status)]])).toEqual({
      shown: "normal",
      cached: "normal",
    });
    expect(toastError).toHaveBeenCalledTimes(1);
  });

  it("puts back the value from before two rejected changes", async () => {
    expect(await run(["large", "x-large", [0, REJECTED], [1, REJECTED]])).toEqual({
      shown: "normal",
      cached: "normal",
    });
  });

  it("puts back an earlier change that saved", async () => {
    expect(await run(["large", "x-large", [0, "saved"], [1, REJECTED]])).toEqual({
      shown: "large",
      cached: "large",
    });
  });

  it("shows an earlier change that saved after a newer one was rejected", async () => {
    expect(await run(["large", "x-large", [1, problem(429)], [0, "saved"]])).toEqual({
      shown: "large",
      cached: "large",
    });
  });

  it("puts back an earlier change that saved while a later one was pending", async () => {
    expect(
      await run(["large", "x-large", [1, REJECTED], "small", [0, "saved"], [2, REJECTED]]),
    ).toEqual({ shown: "large", cached: "large" });
  });

  it("keeps a later change that saved over an earlier one that lands after it", async () => {
    expect(
      await run(["large", "x-large", [1, REJECTED], "small", [2, "saved"], [0, "saved"]]),
    ).toEqual({ shown: "small", cached: "small" });
  });

  // These may have saved; the effective-settings re-read decides what shows.
  it.each([
    ["500", problem(500)],
    ["408", problem(408)],
    ["network", new TypeError("Failed to fetch")],
  ])("keeps the change after an ambiguous %s failure", async (_label, error) => {
    expect(await run(["large", [0, error]])).toEqual({ shown: "large", cached: "large" });
    expect(toastError).not.toHaveBeenCalled();
  });
});
