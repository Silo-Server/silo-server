// @vitest-environment node

import { describe, it, expect } from "vitest";
import { chipsToFilterConfig, filterConfigToChips, type FilterChipModel } from "./filterEasyMode";

describe("filterEasyMode conversion", () => {
  it("converts chips to a flat single-group FilterConfig", () => {
    const chips: FilterChipModel[] = [
      { field: "genre", op: "contains", value: "Sci-Fi" },
      { field: "year", op: "between", value: [1980, 1989] },
    ];
    const config = chipsToFilterConfig(chips, "all");
    expect(config.match).toBe("all");
    expect(config.groups).toHaveLength(1);
    expect(config.groups[0]!.rules).toHaveLength(2);
    expect(config.groups[0]!.match).toBe("all");
    expect(config.groups[0]!.rules[0]).toEqual({ field: "genre", op: "contains", value: "Sci-Fi" });
    expect(config.groups[0]!.rules[1]).toEqual({
      field: "year",
      op: "between",
      value: [1980, 1989],
    });
  });

  it("preserves matchMode when 'any'", () => {
    const config = chipsToFilterConfig([{ field: "genre", op: "contains", value: "Drama" }], "any");
    expect(config.groups[0]!.match).toBe("any");
    expect(config.match).toBe("all"); // top-level always "all" for the wrapper
  });

  it("returns empty groups[0].rules for empty chip array", () => {
    const config = chipsToFilterConfig([], "all");
    expect(config.groups).toHaveLength(1);
    expect(config.groups[0]!.rules).toHaveLength(0);
  });

  it("filterConfigToChips returns chips for a single-group flat config", () => {
    const config = {
      match: "all" as const,
      groups: [
        {
          match: "all" as const,
          rules: [
            { field: "genre", op: "contains", value: "Drama" },
            { field: "rating_imdb", op: "gte", value: 7.0 },
          ],
        },
      ],
    };
    const result = filterConfigToChips(config);
    expect(result.kind).toBe("compatible");
    if (result.kind === "compatible") {
      expect(result.chips).toHaveLength(2);
      expect(result.matchMode).toBe("all");
      expect(result.chips[0]).toEqual({ field: "genre", op: "contains", value: "Drama" });
    }
  });

  it("filterConfigToChips reports incompatibility for multiple groups", () => {
    const config = {
      match: "all" as const,
      groups: [
        { match: "any" as const, rules: [{ field: "genre", op: "contains", value: "X" }] },
        { match: "any" as const, rules: [{ field: "genre", op: "contains", value: "Y" }] },
      ],
    };
    const result = filterConfigToChips(config);
    expect(result.kind).toBe("incompatible");
  });

  it("filterConfigToChips returns empty chips for empty config", () => {
    const result = filterConfigToChips({ match: "all", groups: [] });
    expect(result.kind).toBe("compatible");
    if (result.kind === "compatible") {
      expect(result.chips).toHaveLength(0);
      expect(result.matchMode).toBe("all");
    }
  });

  it("round-trip preserves the chip list and match mode", () => {
    const chips: FilterChipModel[] = [
      { field: "genre", op: "contains", value: "Action" },
      { field: "year", op: "gte", value: 2000 },
    ];
    const config = chipsToFilterConfig(chips, "any");
    const result = filterConfigToChips(config);
    expect(result.kind).toBe("compatible");
    if (result.kind === "compatible") {
      expect(result.chips).toEqual(chips);
      expect(result.matchMode).toBe("any");
    }
  });
});
