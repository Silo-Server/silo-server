import { describe, expect, it } from "vitest";
import { isInNextUpPromptWindow } from "./nextUpPrompt";

describe("isInNextUpPromptWindow", () => {
  it.each([
    { at: 1290, duration: 1320, seconds: 30, expected: true, why: "exactly N seconds left" },
    { at: 1289, duration: 1320, seconds: 30, expected: false, why: "more than N seconds left" },
    { at: 1315, duration: 1320, seconds: 10, expected: true, why: "inside a shorter window" },
    { at: 1250, duration: 1320, seconds: 120, expected: true, why: "inside a longer window" },
    { at: 1319.9, duration: 1320, seconds: 0, expected: false, why: "At end waits for ended" },
    { at: 1320, duration: 1320, seconds: 0, expected: false, why: "At end ignores the tail" },
    { at: 0, duration: 20, seconds: 30, expected: false, why: "not yet started" },
    { at: 10, duration: 0, seconds: 30, expected: false, why: "duration unknown" },
    { at: 10, duration: Infinity, seconds: 30, expected: false, why: "live-like duration" },
    { at: 10, duration: 20, seconds: 30, expected: true, why: "file shorter than the window" },
  ])("$why", ({ at, duration, seconds, expected }) => {
    expect(isInNextUpPromptWindow(at, duration, seconds)).toBe(expected);
  });
});
