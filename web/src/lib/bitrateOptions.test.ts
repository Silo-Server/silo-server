import { describe, expect, it } from "vitest";
import { formatConnectionRateKbps } from "./bitrateOptions";

describe("formatConnectionRateKbps", () => {
  // A lower bound or budget never rounds up past the server's value.
  it.each([
    [44_100, "44 Mbps"],
    [44_999, "44 Mbps"],
    [9_949, "9.9 Mbps"],
    [1_000, "1.0 Mbps"],
    [999, "999 kbps"],
  ])("shows %i kbps as %s", (kbps, text) => {
    expect(formatConnectionRateKbps(kbps)).toBe(text);
  });
});
