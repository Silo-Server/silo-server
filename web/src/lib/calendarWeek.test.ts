// @vitest-environment node

import { describe, expect, it } from "vitest";

import { addDays, addWeeks, getWeekStart } from "./calendarWeek";

describe("calendarWeek", () => {
  it("adds days without rolling into the next week request", () => {
    expect(addDays("2026-04-06", 6)).toBe("2026-04-12");
  });

  it("still advances week starts by full weeks", () => {
    expect(addWeeks("2026-04-06", 1)).toBe("2026-04-13");
  });

  it("normalizes arbitrary dates to the Monday of their week", () => {
    expect(getWeekStart(new Date(2026, 3, 12))).toBe("2026-04-06");
  });
});
