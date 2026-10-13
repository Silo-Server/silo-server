import { describe, expect, it } from "vitest";

import { unshareConsequence, unshareWarning } from "./copy";

describe("unshareConsequence", () => {
  it("agrees with one other profile", () => {
    expect(unshareConsequence(["Leo"])).toBe("Leo loses it, including rows they made from it.");
  });

  it("agrees with several other profiles", () => {
    expect(unshareConsequence(["Maya", "Leo"])).toBe(
      "Maya and Leo lose it, including rows they made from it.",
    );
  });

  it("falls back to other profiles when it has no names", () => {
    expect(unshareConsequence([])).toBe(
      "Other profiles lose it, including rows they made from it.",
    );
  });

  it("carries the agreement into the editor's save warning", () => {
    expect(unshareWarning(["Leo"])).toBe(
      "When you save, Leo loses it, including rows they made from it.",
    );
  });
});
