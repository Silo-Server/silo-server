// @vitest-environment node

import { describe, expect, it } from "vitest";
import { formatDeviceCode, spokenDeviceCode } from "./deviceCode";

describe("device codes", () => {
  it("groups the code 4+4, upper-cased, keeping only its characters", () => {
    expect(formatDeviceCode("abcd1234")).toBe("ABCD 1234");
    expect(formatDeviceCode("ab-cd 12")).toBe("ABCD 12");
    expect(formatDeviceCode("abc")).toBe("ABC");
    expect(formatDeviceCode("ABCD12345678")).toBe("ABCD 1234");
  });

  it("spells the code one character at a time", () => {
    expect(spokenDeviceCode("ABCD 1234")).toBe("A B C D 1 2 3 4");
    expect(spokenDeviceCode("4821-7730")).toBe("4 8 2 1 7 7 3 0");
  });
});
