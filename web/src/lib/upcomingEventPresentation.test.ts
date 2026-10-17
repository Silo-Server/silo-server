// @vitest-environment node

import { describe, expect, it } from "vitest";
import { formatUpcomingTime, upcomingBadgeLabel } from "./upcomingEventPresentation";

describe("upcomingBadgeLabel", () => {
  it("uses the same badge wording as the native apps", () => {
    expect(upcomingBadgeLabel("series_premiere")).toBe("Series Premiere");
    expect(upcomingBadgeLabel("season_premiere")).toBe("New Season");
    expect(upcomingBadgeLabel("finale")).toBe("Finale");
    expect(upcomingBadgeLabel("something_new")).toBe("something_new");
  });
});

describe("formatUpcomingTime", () => {
  it("formats air_at before falling back to source air_time", () => {
    const expected = new Date("2026-01-01T15:30:00Z").toLocaleTimeString(undefined, {
      hour: "numeric",
      minute: "2-digit",
    });

    expect(formatUpcomingTime("00:30", "2026-01-01T15:30:00Z")).toBe(expected);
  });

  it("falls back to source air_time when air_at is missing", () => {
    const expected = new Date("2000-01-01T20:00").toLocaleTimeString(undefined, {
      hour: "numeric",
      minute: "2-digit",
    });

    expect(formatUpcomingTime("20:00", null)).toBe(expected);
  });
});
