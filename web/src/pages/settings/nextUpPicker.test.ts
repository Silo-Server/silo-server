import { describe, expect, it } from "vitest";
import { SETTING_KEYS } from "@/lib/settingsContract";
import { nextUpPickerChoices, nextUpPickerValue, nextUpPickerWrites } from "./nextUpPicker";

const ENABLED = SETTING_KEYS.PLAYBACK_NEXT_UP_ENABLED;
const SECONDS = SETTING_KEYS.PLAYBACK_NEXT_UP_PROMPT_SECONDS;

describe("Show Next Up picker", () => {
  it("offers Off first where the server defines the setting", () => {
    expect(nextUpPickerChoices(true)).toEqual(["off", "0", "10", "30", "60", "120"]);
    expect(nextUpPickerChoices(false)).toEqual(["0", "10", "30", "60", "120"]);
  });

  it.each([
    { enabled: false, seconds: 30, expected: "off" },
    { enabled: true, seconds: 30, expected: "30" },
  ])("shows $expected for enabled=$enabled seconds=$seconds", ({ expected, ...state }) => {
    expect(nextUpPickerValue(state)).toBe(expected);
  });

  it.each([
    { choice: "off", offOffered: true, expected: [[ENABLED, false]] },
    {
      choice: "60",
      offOffered: true,
      expected: [
        [SECONDS, 60],
        [ENABLED, true],
      ],
    },
    { choice: "0", offOffered: false, expected: [[SECONDS, 0]] },
  ])("choosing $choice writes $expected", ({ choice, offOffered, expected }) => {
    expect(nextUpPickerWrites(choice, offOffered)).toEqual(expected);
  });
});
