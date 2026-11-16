import { expect, it } from "vitest";
import type { AppNotification } from "@/api/types";
import { groupInbox, inboxDay } from "./inboxGroups";

it("buckets by local calendar day, not by 24-hour spans", () => {
  const now = new Date(2026, 10, 16, 0, 30);
  expect(inboxDay(new Date(2026, 10, 16, 0, 5), now)).toBe("today");
  // Forty minutes earlier, but on the previous calendar day.
  expect(inboxDay(new Date(2026, 10, 15, 23, 50), now)).toBe("yesterday");
  expect(inboxDay(new Date(2026, 10, 10, 12, 0), now)).toBe("week");
  expect(inboxDay(new Date(2026, 10, 9, 12, 0), now)).toBe("older");
});

it("groups a series' episodes within a day only", () => {
  const now = new Date(2026, 10, 16, 12, 0);
  const at = (id: string, series: string | undefined, date: Date, type = "episode.available") =>
    ({
      id,
      type,
      series_id: series,
      created_at: date.toISOString(),
      reason_flags: {},
      read_at: null,
    }) as unknown as AppNotification;
  const today = new Date(2026, 10, 16, 10, 0);
  const yesterday = new Date(2026, 10, 15, 10, 0);
  const sections = groupInbox(
    [
      at("a2", "series:a", today),
      at("req", "series:a", today, "request.fulfilled"),
      at("a1", "series:a", today),
      at("a0", "series:a", yesterday),
    ],
    now,
  );
  expect(
    sections.map((section) => [
      section.day,
      section.entries.map((entry) => entry.notifications.map((n) => n.id)),
    ]),
  ).toEqual([
    ["today", [["a2", "a1"], ["req"]]],
    ["yesterday", [["a0"]]],
  ]);
});
