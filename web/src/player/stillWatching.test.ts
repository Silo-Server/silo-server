import { beforeEach, describe, expect, it } from "vitest";

import { storage } from "@/utils/storage";
import {
  getStillWatchingThreshold,
  isStillWatchingGated,
  recordAutoAdvance,
  resetStillWatchingStreak,
  setStillWatchingThreshold,
} from "./stillWatching";

describe("still watching streak", () => {
  beforeEach(() => resetStillWatchingStreak());

  it("allows N automatic advances and gates the one after", () => {
    setStillWatchingThreshold(3);
    recordAutoAdvance();
    recordAutoAdvance();
    expect(isStillWatchingGated()).toBe(false);
    recordAutoAdvance();
    expect(isStillWatchingGated()).toBe(true);
  });

  it("clears the streak on a deliberate action", () => {
    setStillWatchingThreshold(2);
    recordAutoAdvance();
    recordAutoAdvance();
    expect(isStillWatchingGated()).toBe(true);
    resetStillWatchingStreak();
    expect(isStillWatchingGated()).toBe(false);
  });

  it("never gates at threshold 0", () => {
    setStillWatchingThreshold(0);
    for (let i = 0; i < 25; i++) recordAutoAdvance();
    expect(isStillWatchingGated()).toBe(false);
  });
});

describe("still watching threshold", () => {
  it.each([
    [null, 3],
    ["0", 0],
    ["20", 20],
    ["21", 3],
  ])("reads stored value %s as %i", (stored, expected) => {
    if (stored === null) storage.remove(storage.KEYS.STILL_WATCHING_THRESHOLD);
    else storage.set(storage.KEYS.STILL_WATCHING_THRESHOLD, stored);
    expect(getStillWatchingThreshold()).toBe(expected);
  });
});
