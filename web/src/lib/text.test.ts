import { describe, expect, it } from "vitest";
import { getInitials } from "./text";

// Names that start with a character outside the Basic Multilingual Plane are two
// UTF-16 code units; cutting between them draws a replacement glyph.
describe("getInitials", () => {
  it.each([
    ["\u{1F43B} Bear", "\u{1F43B}B"],
    ["\u{1F43B}Bear", "\u{1F43B}B"],
  ])("keeps %j's first character whole", (name, initials) => {
    expect(getInitials(name)).toBe(initials);
  });
});
