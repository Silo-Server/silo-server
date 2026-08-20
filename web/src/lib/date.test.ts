import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { formatRelativeTime } from "./date";
import { setDateTimeFormatPreferences } from "./datetime";

const NOW = new Date(2026, 5, 5, 12, 0, 0);

function ago(ms: number): string {
  return new Date(NOW.getTime() - ms).toISOString();
}

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});

afterEach(() => {
  vi.useRealTimers();
  setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "auto" });
});

describe("formatRelativeTime", () => {
  it("returns null for missing or unparseable values", () => {
    expect(formatRelativeTime(null)).toBeNull();
    expect(formatRelativeTime(undefined)).toBeNull();
    expect(formatRelativeTime("")).toBeNull();
    expect(formatRelativeTime("not-a-date")).toBeNull();
  });

  it("formats the compact tiers", () => {
    expect(formatRelativeTime(ago(10_000))).toBe("just now");
    expect(formatRelativeTime(ago(5 * MINUTE))).toBe("5m ago");
    expect(formatRelativeTime(ago(3 * HOUR))).toBe("3h ago");
    expect(formatRelativeTime(ago(2 * DAY))).toBe("2d ago");
  });

  it("treats future timestamps as just now", () => {
    expect(formatRelativeTime(ago(-5 * MINUTE))).toBe("just now");
  });

  it("rounds by default and floors when asked", () => {
    // 1h31m: round → 2h, floor → 1h.
    const value = ago(HOUR + 31 * MINUTE);
    expect(formatRelativeTime(value)).toBe("2h ago");
    expect(formatRelativeTime(value, { rounding: "floor" })).toBe("1h ago");
  });

  it("derives each tier from the raw delta, not the rounded coarser tier", () => {
    // 89.5 minutes is 1.49h: rounding the minute count first (90m) would
    // compound to "2h ago"; the raw delta keeps it at "1h ago".
    expect(formatRelativeTime(ago(89.5 * MINUTE))).toBe("1h ago");
  });

  it("supports a custom just-now label", () => {
    expect(formatRelativeTime(ago(10_000), { justNowLabel: "Just now" })).toBe("Just now");
  });

  it("switches to an absolute date after the configured number of days", () => {
    setDateTimeFormatPreferences({ dateFormat: "YYYY-MM-DD", timeFormat: "auto" });
    expect(formatRelativeTime(ago(29 * DAY), { absoluteAfterDays: 30 })).toBe("29d ago");
    expect(formatRelativeTime(ago(40 * DAY), { absoluteAfterDays: 30 })).toBe("2026-04-26");
    // absoluteAfterDays: 1 skips the day tier entirely (floor keeps 25h at 1d).
    expect(formatRelativeTime(ago(25 * HOUR), { rounding: "floor", absoluteAfterDays: 1 })).toBe(
      "2026-06-04",
    );
  });

  it("uses a caller-provided absolute renderer", () => {
    expect(
      formatRelativeTime(ago(40 * DAY), {
        absoluteAfterDays: 30,
        absolute: (date) => `on ${date.getFullYear()}`,
      }),
    ).toBe("on 2026");
  });
});
