import { describe, expect, it } from "vitest";
import { formatBitrateKbps, formatConnectionRateKbps } from "./bitrateOptions";

describe("formatConnectionRateKbps", () => {
  // The spec's examples (limit 44100, sample 51200, budget 35280) read as
  // whole Mbps, and a lower bound or budget never rounds up past the server's
  // value.
  it.each([
    [44_100, "44 Mbps"],
    [51_200, "51 Mbps"],
    [35_280, "35 Mbps"],
    [114_600, "114 Mbps"],
    [44_999, "44 Mbps"],
    [9_949, "9.9 Mbps"],
    [1_400, "1.4 Mbps"],
    [5_000, "5.0 Mbps"],
    [1_000, "1.0 Mbps"],
    [999, "999 kbps"],
    [450, "450 kbps"],
    [1_000_000, "1000 Mbps"],
  ])("shows %i kbps as %s", (kbps, text) => {
    expect(formatConnectionRateKbps(kbps)).toBe(text);
  });

  it("leaves the Maximum bitrate choices as they read", () => {
    expect(formatBitrateKbps(1_500)).toBe("1.5 Mbps");
    expect(formatBitrateKbps(44_100)).toBe("44.1 Mbps");
  });
});
