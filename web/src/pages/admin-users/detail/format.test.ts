// @vitest-environment node

import { afterEach, describe, expect, it, vi } from "vitest";

import { formatStorageLimit, watchedFraction } from "./format";

afterEach(() => {
  vi.useRealTimers();
});

describe("watchedFraction", () => {
  it("counts a completed play as all of it", () => {
    expect(watchedFraction(10, 100, true)).toBe(1);
    expect(watchedFraction(10, null, true)).toBe(1);
  });
  it("divides by the duration and clamps", () => {
    expect(watchedFraction(38, 100, false)).toBeCloseTo(0.38);
    expect(watchedFraction(150, 100, false)).toBe(1);
  });
  it("is 0 without a usable duration", () => {
    expect(watchedFraction(10, null, false)).toBe(0);
    expect(watchedFraction(10, 0, false)).toBe(0);
  });
});

describe("formatStorageLimit", () => {
  it("reads a cap in the binary gigabytes the apps store", () => {
    expect(formatStorageLimit(10 * 1024 ** 3)).toBe("10 GB");
    expect(formatStorageLimit(1.5 * 1024 ** 3)).toBe("1.5 GB");
    expect(formatStorageLimit(512 * 1024 ** 2)).toBe("512 MB");
  });
});
