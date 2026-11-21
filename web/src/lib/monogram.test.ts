import { describe, expect, it } from "vitest";
import { providerMonogram } from "./monogram";

// A combining mark belongs to the letter before it: "e" + U+0301 is "é", and a
// Devanagari vowel sign is part of its syllable. Dropping marks changes the letter.
describe("providerMonogram", () => {
  it.each([
    ["éclair", "ÉC"],
    ["éclair", "ÉC"],
    // हिंदी: two characters, हिं and दी
    ["हिंदी", "हिंदी"],
    // वाक् संवाद: the word-final virama must not join the next word's letter
    ["वाक् संवाद", "वाक्"],
    ["AniList", "AN"],
    ["", "??"],
  ])("marks %j as %j", (name, monogram) => {
    expect(providerMonogram(name)).toBe(monogram);
  });
});
