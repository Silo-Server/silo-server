import { describe, expect, it } from "vitest";

import { updateCheckboxSelection } from "./checkboxSelection";

describe("updateCheckboxSelection", () => {
  const order = ["one", "two", "three", "four"];

  it("toggles one item without replacing the existing selection", () => {
    expect([
      ...updateCheckboxSelection(new Set(["one"]), order, "one", "three", true, false),
    ]).toEqual(["one", "three"]);
    expect([
      ...updateCheckboxSelection(new Set(["one", "three"]), order, "three", "one", false, false),
    ]).toEqual(["three"]);
  });

  it("selects an inclusive range when shift-clicking an unchecked item", () => {
    expect([
      ...updateCheckboxSelection(new Set(["one"]), order, "one", "three", true, true),
    ]).toEqual(["one", "two", "three"]);
  });

  it("clears an inclusive range when shift-clicking a checked item", () => {
    expect([
      ...updateCheckboxSelection(
        new Set(["one", "two", "three", "four"]),
        order,
        "one",
        "three",
        false,
        true,
      ),
    ]).toEqual(["four"]);
  });

  it("falls back to changing one item when the anchor is no longer visible", () => {
    expect([
      ...updateCheckboxSelection(new Set(["outside"]), order, "outside", "three", true, true),
    ]).toEqual(["outside", "three"]);
  });

  it("uses visual row occurrences when one item appears more than once", () => {
    const rows = ["1:shared", "1:alpha", "2:beta", "2:shared"];
    const collectionIdByRow = new Map([
      ["1:shared", "shared"],
      ["1:alpha", "alpha"],
      ["2:beta", "beta"],
      ["2:shared", "shared"],
    ]);

    expect([
      ...updateCheckboxSelection(
        new Set(["beta"]),
        rows,
        "2:beta",
        "2:shared",
        true,
        true,
        (rowId) => collectionIdByRow.get(rowId) ?? rowId,
      ),
    ]).toEqual(["beta", "shared"]);
  });
});
