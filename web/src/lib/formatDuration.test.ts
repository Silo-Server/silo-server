// @vitest-environment node

import { describe, expect, it } from "vitest";

import { formatDuration } from "./formatDuration";

describe("formatDuration", () => {
  it("formats hours and minutes", () => {
    expect(formatDuration(7800)).toBe("2h 10m");
  });

  it("formats minutes only, under an hour", () => {
    expect(formatDuration(2700)).toBe("45m");
  });

  it("formats an exact hour with zero minutes", () => {
    expect(formatDuration(3600)).toBe("1h 0m");
  });

  it("floors partial minutes", () => {
    expect(formatDuration(119)).toBe("1m");
  });

  it("returns <1m for durations under a minute", () => {
    expect(formatDuration(45)).toBe("<1m");
  });

  it("returns <1m for zero seconds", () => {
    expect(formatDuration(0)).toBe("<1m");
  });

  it("returns <1m just under the one-minute boundary", () => {
    expect(formatDuration(59)).toBe("<1m");
  });

  it("formats exactly one minute", () => {
    expect(formatDuration(60)).toBe("1m");
  });
});
