import { describe, expect, it } from "vitest";

import {
  STREAM_BITRATE_LIMIT_PRESETS_KBPS,
  formatStreamBitrateLimit,
  isLowStreamBitrateLimit,
  parseStreamBitrateMbps,
} from "./streamBitrateLimit";

describe("formatStreamBitrateLimit", () => {
  it("shows 0 as unlimited and every cap in exact Mbps", () => {
    expect(formatStreamBitrateLimit(0)).toBe("Unlimited");
    expect(formatStreamBitrateLimit(8000)).toBe("8 Mbps");
    expect(formatStreamBitrateLimit(1500)).toBe("1.5 Mbps");
    expect(formatStreamBitrateLimit(1234)).toBe("1.234 Mbps");
    expect(formatStreamBitrateLimit(500)).toBe("0.5 Mbps");
    expect(formatStreamBitrateLimit(20)).toBe("0.02 Mbps");
  });

  it("round-trips every stored kbps value through the custom box", () => {
    for (const kbps of [1, 20, 999, 1001, 4500, 123456, ...STREAM_BITRATE_LIMIT_PRESETS_KBPS]) {
      expect(parseStreamBitrateMbps(String(kbps / 1000))).toBe(kbps);
    }
  });
});

describe("parseStreamBitrateMbps", () => {
  it("converts Mbps to whole kbps", () => {
    expect(parseStreamBitrateMbps("20")).toBe(20000);
    expect(parseStreamBitrateMbps("1.5")).toBe(1500);
    expect(parseStreamBitrateMbps(" 0.5 ")).toBe(500);
    expect(parseStreamBitrateMbps(".25")).toBe(250);
    expect(parseStreamBitrateMbps("1,5")).toBe(1500);
    expect(parseStreamBitrateMbps("1.005")).toBe(1005);
    expect(parseStreamBitrateMbps("8.")).toBe(8000);
  });

  it("rejects text it cannot store exactly instead of rounding", () => {
    expect(parseStreamBitrateMbps("")).toBeNull();
    expect(parseStreamBitrateMbps("abc")).toBeNull();
    expect(parseStreamBitrateMbps("-1")).toBeNull();
    expect(parseStreamBitrateMbps("1e3")).toBeNull();
    expect(parseStreamBitrateMbps("0.0005")).toBeNull();
    expect(parseStreamBitrateMbps("1.2.3")).toBeNull();
  });

  it("rejects 0, which means unlimited rather than a cap", () => {
    expect(parseStreamBitrateMbps("0")).toBeNull();
    expect(parseStreamBitrateMbps("0.")).toBeNull();
    expect(parseStreamBitrateMbps("0.000")).toBeNull();
  });
});

describe("isLowStreamBitrateLimit", () => {
  it("flags caps under 1 Mbps but not unlimited", () => {
    expect(isLowStreamBitrateLimit(0)).toBe(false);
    expect(isLowStreamBitrateLimit(20)).toBe(true);
    expect(isLowStreamBitrateLimit(999)).toBe(true);
    expect(isLowStreamBitrateLimit(1000)).toBe(false);
  });
});
