import { describe, expect, it } from "vitest";
import { isInNextUpPromptWindow } from "./nextUpPrompt";

describe("isInNextUpPromptWindow", () => {
  it.each([
    { at: 1290, duration: 1320, seconds: 30, expected: true, why: "exactly N seconds left" },
    { at: 1289, duration: 1320, seconds: 30, expected: false, why: "more than N seconds left" },
    { at: 1319.9, duration: 1320, seconds: 0, expected: false, why: "At end waits for ended" },
    { at: 0, duration: 20, seconds: 30, expected: false, why: "not yet started" },
    { at: 10, duration: 0, seconds: 30, expected: false, why: "duration unknown" },
  ])("$why", ({ at, duration, seconds, expected }) => {
    expect(isInNextUpPromptWindow(at, duration, seconds)).toBe(expected);
  });
});
