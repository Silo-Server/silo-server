import { afterEach, describe, expect, it } from "vitest";
import {
  formatDate,
  formatDateTime,
  formatTime,
  getDateTimeFormatPreferences,
  parseDateFormatPreference,
  parseTimeFormatPreference,
  preferredDateLocale,
  setDateTimeFormatPreferences,
  subscribeDateTimeFormatPreferences,
} from "./datetime";

// June 5, 2026 15:04:05 in local time — unambiguous day/month for order checks.
const sample = new Date(2026, 5, 5, 15, 4, 5);

afterEach(() => {
  setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "auto" });
});

describe("parse helpers", () => {
  it("accepts known values and falls back to auto", () => {
    expect(parseDateFormatPreference("DD/MM/YYYY")).toBe("DD/MM/YYYY");
    expect(parseDateFormatPreference("YYYY-MM-DD")).toBe("YYYY-MM-DD");
    expect(parseDateFormatPreference("bogus")).toBe("auto");
    expect(parseDateFormatPreference(null)).toBe("auto");
    expect(parseTimeFormatPreference("24h")).toBe("24h");
    expect(parseTimeFormatPreference("12H")).toBe("auto");
    expect(parseTimeFormatPreference(undefined)).toBe("auto");
  });
});

describe("formatDate", () => {
  it("honors explicit numeric patterns", () => {
    setDateTimeFormatPreferences({ dateFormat: "DD/MM/YYYY", timeFormat: "auto" });
    expect(formatDate(sample)).toBe("05/06/2026");

    setDateTimeFormatPreferences({ dateFormat: "MM/DD/YYYY", timeFormat: "auto" });
    expect(formatDate(sample)).toBe("06/05/2026");

    setDateTimeFormatPreferences({ dateFormat: "YYYY-MM-DD", timeFormat: "auto" });
    expect(formatDate(sample)).toBe("2026-06-05");
  });

  it("uses the browser locale in auto mode", () => {
    expect(formatDate(sample)).toBe(sample.toLocaleDateString());
  });

  it("orders medium (month-name) dates to match the preference", () => {
    setDateTimeFormatPreferences({ dateFormat: "MM/DD/YYYY", timeFormat: "auto" });
    expect(formatDate(sample, "medium")).toBe("Jun 5, 2026");

    setDateTimeFormatPreferences({ dateFormat: "DD/MM/YYYY", timeFormat: "auto" });
    expect(formatDate(sample, "medium")).toBe("5 Jun 2026");

    setDateTimeFormatPreferences({ dateFormat: "YYYY-MM-DD", timeFormat: "auto" });
    expect(formatDate(sample, "medium")).toBe("2026-06-05");
  });

  it("accepts ISO strings and returns empty for invalid input", () => {
    setDateTimeFormatPreferences({ dateFormat: "YYYY-MM-DD", timeFormat: "auto" });
    expect(formatDate(sample.toISOString())).toBe("2026-06-05");
    expect(formatDate("not-a-date")).toBe("");
  });
});

describe("formatTime", () => {
  it("honors the 12h/24h preference", () => {
    setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "24h" });
    expect(formatTime(sample)).toBe("15:04");

    setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "12h" });
    expect(formatTime(sample)).toMatch(/^3:04\sPM$/i);
  });

  it("zero-pads single-digit hours in 24h mode", () => {
    setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "24h" });
    const morning = new Date(2026, 5, 5, 9, 4, 5);
    expect(formatTime(morning)).toBe("09:04");
  });

  it("merges extra options such as seconds", () => {
    setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "24h" });
    expect(formatTime(sample, { second: "2-digit" })).toBe("15:04:05");
  });

  it("returns empty for invalid input", () => {
    expect(formatTime("nope")).toBe("");
  });
});

describe("formatDateTime", () => {
  it("combines the preferred date and time with seconds by default", () => {
    setDateTimeFormatPreferences({ dateFormat: "DD/MM/YYYY", timeFormat: "24h" });
    expect(formatDateTime(sample)).toBe("05/06/2026, 15:04:05");
  });

  it("supports medium dates without seconds", () => {
    setDateTimeFormatPreferences({ dateFormat: "MM/DD/YYYY", timeFormat: "24h" });
    expect(formatDateTime(sample, { dateStyle: "medium", seconds: false })).toBe(
      "Jun 5, 2026, 15:04",
    );
  });
});

describe("preference store", () => {
  it("exposes the preferred locale for composed fragments", () => {
    expect(preferredDateLocale()).toBeUndefined();
    setDateTimeFormatPreferences({ dateFormat: "DD/MM/YYYY", timeFormat: "auto" });
    expect(preferredDateLocale()).toBe("en-GB");
    setDateTimeFormatPreferences({ dateFormat: "MM/DD/YYYY", timeFormat: "auto" });
    expect(preferredDateLocale()).toBe("en-US");
    setDateTimeFormatPreferences({ dateFormat: "YYYY-MM-DD", timeFormat: "auto" });
    expect(preferredDateLocale()).toBeUndefined();
  });

  it("notifies subscribers only on actual changes", () => {
    let calls = 0;
    const unsubscribe = subscribeDateTimeFormatPreferences(() => {
      calls += 1;
    });
    setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "auto" });
    expect(calls).toBe(0);
    setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "24h" });
    expect(calls).toBe(1);
    expect(getDateTimeFormatPreferences()).toEqual({ dateFormat: "auto", timeFormat: "24h" });
    unsubscribe();
    setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "12h" });
    expect(calls).toBe(1);
  });
});
