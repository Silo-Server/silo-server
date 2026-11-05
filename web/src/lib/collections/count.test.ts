import { describe, expect, it } from "vitest";
import { collectionCountLabel, collectionItemCount } from "./count";

describe("collection counts", () => {
  it.each([
    [undefined, 7, 7],
    [undefined, 0, 0],
    ["available", 0, 0],
    ["available", 7, 7],
    ["unavailable", 0, null],
    ["unavailable", 91, null],
    ["future-status", 91, null],
    ["", 91, null],
  ])("decodes status %s with count %s as %s", (item_count_status, item_count, expected) => {
    const count = collectionItemCount({ item_count_status, item_count });
    expect(count).toBe(expected);
    expect(collectionCountLabel(count)).toBe(
      expected === null ? "Count unavailable" : `${expected} titles`,
    );
  });

  it("uses each fresh response, so an unavailable count can recover to a real zero", () => {
    expect(collectionItemCount({ item_count: 32, item_count_status: "unavailable" })).toBeNull();
    expect(collectionItemCount({ item_count: 0, item_count_status: "available" })).toBe(0);
  });
});
