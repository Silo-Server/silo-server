import { describe, expect, it } from "vitest";

import { cn } from "./utils";

describe("cn", () => {
  // A later shorthand replaces an earlier logical side, as it does a physical one.
  it.each([
    ["ps-2 px-4", "px-4"],
    ["me-2 mx-auto", "mx-auto"],
    ["start-2 inset-x-0", "inset-x-0"],
    ["border-s-2 border-x-0", "border-x-0"],
    ["border-e-red-500 border-x-transparent", "border-x-transparent"],
    ["px-4 ps-2", "px-4 ps-2"],
  ])("%s → %s", (input, merged) => {
    expect(cn(input)).toBe(merged);
  });
});
