import { describe, expect, it } from "vitest";
import { applyAdminLogAppend, applyAdminLogAppends } from "./useAdminLogStream";

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

describe("applyAdminLogAppends", () => {
  it("batches appends while keeping newest entries first", () => {
    expect(
      applyAdminLogAppends(
        [
          { id: 3, message: "current newest" },
          { id: 1, message: "oldest" },
        ],
        [
          { id: 4, message: "next" },
          { id: 3, message: "updated current newest" },
          { id: 5, message: "latest" },
        ],
        4,
      ),
    ).toEqual([
      { id: 5, message: "latest" },
      { id: 3, message: "updated current newest" },
      { id: 4, message: "next" },
      { id: 1, message: "oldest" },
    ]);
  });
});
