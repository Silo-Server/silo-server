import { describe, expect, it } from "vitest";

import { formatDataTotal } from "./format";

describe("formatDataTotal", () => {
  it("reads missing or invalid totals as unknown", () => {
    expect(formatDataTotal(undefined)).toBe("—");
    expect(formatDataTotal(null)).toBe("—");
    expect(formatDataTotal(Number.NaN)).toBe("—");
    expect(formatDataTotal(-1)).toBe("—");
  });

  it("keeps whole bytes for tiny totals", () => {
    expect(formatDataTotal(0)).toBe("0 B");
    expect(formatDataTotal(999)).toBe("999 B");
  });

  it("uses decimal units with one decimal below 100", () => {
    expect(formatDataTotal(1_500)).toBe("1.5 KB");
    expect(formatDataTotal(42_300_000_000)).toBe("42.3 GB");
    expect(formatDataTotal(1_400_000_000_000)).toBe("1.4 TB");
  });

  it("drops the decimal once the number is large", () => {
    expect(formatDataTotal(512_000_000_000)).toBe("512 GB");
  });

  it("chooses unit and precision after rounding", () => {
    // Rounds up across a unit boundary: never "1,000 KB" or "1,000 B".
    expect(formatDataTotal(999_950)).toBe("1.0 MB");
    expect(formatDataTotal(999_999_999_999)).toBe("1.0 TB");
    // Rounds up to 100: the decimal goes, never "100.0 GB".
    expect(formatDataTotal(99_960_000_000)).toBe("100 GB");
    expect(formatDataTotal(99_940_000_000)).toBe("99.9 GB");
  });

  it("stays in petabytes rather than running off the unit list", () => {
    expect(formatDataTotal(5_000_000_000_000_000_000)).toBe("5,000 PB");
  });
});
