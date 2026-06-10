import { beforeEach, describe, expect, it, vi } from "vitest";
import { storage } from "@/utils/storage";
import {
  clampAudiobookRate,
  getAudiobookSkipBack,
  getAudiobookSkipForward,
  getAudiobookSmartRewind,
  getBookRate,
  setBookRate,
} from "./useAudiobookPrefs";

beforeEach(() => {
  // The vitest jsdom environment ships a non-functional localStorage; install
  // an in-memory one per test, matching the convention in api/client.test.ts.
  const state = new Map<string, string>();
  Object.defineProperty(globalThis, "localStorage", {
    value: {
      get length() {
        return state.size;
      },
      getItem: (key: string) => state.get(key) ?? null,
      key: (index: number) => Array.from(state.keys())[index] ?? null,
      setItem: (key: string, value: string) => {
        state.set(key, value);
      },
      removeItem: (key: string) => {
        state.delete(key);
      },
      clear: () => {
        state.clear();
      },
    } satisfies Storage,
    configurable: true,
  });
});

describe("skip interval prefs", () => {
  it("returns asymmetric defaults", () => {
    expect(getAudiobookSkipBack()).toBe(10);
    expect(getAudiobookSkipForward()).toBe(30);
  });

  it("reads persisted values", () => {
    storage.set(storage.KEYS.AUDIOBOOK_SKIP_BACK, "15");
    storage.set(storage.KEYS.AUDIOBOOK_SKIP_FORWARD, "60");
    expect(getAudiobookSkipBack()).toBe(15);
    expect(getAudiobookSkipForward()).toBe(60);
  });

  it("falls back to defaults for values outside the allowed set", () => {
    storage.set(storage.KEYS.AUDIOBOOK_SKIP_BACK, "7");
    storage.set(storage.KEYS.AUDIOBOOK_SKIP_FORWARD, "garbage");
    expect(getAudiobookSkipBack()).toBe(10);
    expect(getAudiobookSkipForward()).toBe(30);
  });
});

describe("smart rewind pref", () => {
  it("defaults to enabled", () => {
    expect(getAudiobookSmartRewind()).toBe(true);
  });

  it("only an explicit false disables it", () => {
    storage.set(storage.KEYS.AUDIOBOOK_SMART_REWIND, "false");
    expect(getAudiobookSmartRewind()).toBe(false);
    storage.set(storage.KEYS.AUDIOBOOK_SMART_REWIND, "true");
    expect(getAudiobookSmartRewind()).toBe(true);
  });
});

describe("clampAudiobookRate", () => {
  it("clamps to the supported range", () => {
    expect(clampAudiobookRate(0.1)).toBe(0.5);
    expect(clampAudiobookRate(9)).toBe(3);
  });

  it("snaps to the 0.05 grid without float drift", () => {
    expect(clampAudiobookRate(1.2500000000003)).toBe(1.25);
    expect(clampAudiobookRate(1.23)).toBe(1.25);
  });

  it("treats invalid input as 1x", () => {
    expect(clampAudiobookRate(NaN)).toBe(1);
  });
});

describe("per-book rate memory", () => {
  it("round-trips a rate per book", () => {
    expect(getBookRate("book-a")).toBeNull();
    setBookRate("book-a", 1.5);
    setBookRate("book-b", 2);
    expect(getBookRate("book-a")).toBe(1.5);
    expect(getBookRate("book-b")).toBe(2);
  });

  it("evicts the oldest entries beyond the cap", () => {
    const now = vi.spyOn(Date, "now");
    for (let i = 0; i < 55; i++) {
      // Distinct timestamps make eviction order deterministic.
      now.mockReturnValue(1_000_000 + i);
      setBookRate(`book-${i}`, 1.25);
    }
    now.mockRestore();
    expect(getBookRate("book-0")).toBeNull();
    expect(getBookRate("book-4")).toBeNull();
    expect(getBookRate("book-5")).toBe(1.25);
    expect(getBookRate("book-54")).toBe(1.25);
  });

  it("tolerates malformed stored JSON", () => {
    storage.set(storage.KEYS.AUDIOBOOK_RATES, "{not json");
    expect(getBookRate("book-a")).toBeNull();
    setBookRate("book-a", 1.75);
    expect(getBookRate("book-a")).toBe(1.75);
  });
});
