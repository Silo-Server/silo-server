import { describe, expect, it } from "vitest";
import { applyAdminLogAppend } from "./useAdminLogStream";

describe("applyAdminLogAppend", () => {
  it("prepends new rows, dedupes by id, and enforces the limit", () => {
    expect(
      applyAdminLogAppend(
        [
          { id: 2, message: "older" },
          { id: 1, message: "oldest" },
        ],
        { id: 3, message: "new" },
        2,
      ),
    ).toEqual([
      { id: 3, message: "new" },
      { id: 2, message: "older" },
    ]);

    expect(
      applyAdminLogAppend(
        [
          { id: 2, message: "older" },
          { id: 1, message: "oldest" },
        ],
        { id: 2, message: "updated" },
        5,
      ),
    ).toEqual([
      { id: 2, message: "updated" },
      { id: 1, message: "oldest" },
    ]);
  });
});
