// @vitest-environment node

import { describe, expect, it } from "vitest";
import {
  displayDeviceCode,
  formatDeviceCode,
  isCompleteDeviceCode,
  spokenDeviceCode,
  tooManyCodesMessage,
} from "./deviceCode";

describe("device codes", () => {
  it("groups the code 4+4, upper-cased, keeping only its characters", () => {
    expect(formatDeviceCode("abcd1234")).toBe("ABCD 1234");
    expect(formatDeviceCode("ab-cd 12")).toBe("ABCD 12");
    expect(formatDeviceCode("abc")).toBe("ABC");
    expect(formatDeviceCode("ABCD12345678")).toBe("ABCD 1234");
  });

  it("accepts eight-character device codes and six-digit Quick Connect codes", () => {
    expect(isCompleteDeviceCode("48217730")).toBe(true);
    expect(isCompleteDeviceCode("482177")).toBe(true);
    expect(isCompleteDeviceCode("4821773")).toBe(false);
    expect(isCompleteDeviceCode("48217")).toBe(false);
  });

  it("shows a Quick Connect code ungrouped, as the Jellyfin app does", () => {
    expect(displayDeviceCode("482177")).toBe("482177");
    expect(displayDeviceCode("4821-7730")).toBe("4821 7730");
  });

  it("says how long a spent Quick Connect code budget lasts", () => {
    expect(tooManyCodesMessage(900)).toBe("You've tried too many codes. Try again in 15 minutes.");
    expect(tooManyCodesMessage(30)).toBe("You've tried too many codes. Try again in 1 minute.");
    expect(tooManyCodesMessage(null)).toBe("You've tried too many codes. Try again later.");
  });

  it("spells the code one character at a time", () => {
    expect(spokenDeviceCode("ABCD 1234")).toBe("A B C D 1 2 3 4");
    expect(spokenDeviceCode("4821-7730")).toBe("4 8 2 1 7 7 3 0");
  });
});
