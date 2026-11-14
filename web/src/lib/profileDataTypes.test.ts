// @vitest-environment node

import { describe, expect, it } from "vitest";

import { pendingSummary } from "./profileDataTypes";

describe("pendingSummary", () => {
  it.each([
    [
      "sums pending across profiles, counting a new profile's entries in full",
      [
        { id: "ratings" as const, count: 40, pending: 3 },
        { id: "watch_history" as const, count: 900, pending: 10 },
        { id: "watch_history" as const, count: 2 },
      ],
      "12 plays · 3 ratings to add",
    ],
    [
      "says nothing when nothing would change",
      [{ id: "ratings" as const, count: 40, pending: 0 }],
      null,
    ],
  ])("%s", (_case, data, expected) => {
    expect(pendingSummary(data)).toBe(expected);
  });
});
