import { describe, expect, it } from "vitest";

import { parseRankLadder } from "./vendorBaseline";

describe("parseRankLadder", () => {
  it("groups entries into ordered tiers and maps the empty label to Any", () => {
    const tiers = parseRankLadder('rank := {\n\t"": 0,\n\t"480P": 1,\n\t"720P": 2,\n}');
    expect(tiers).toEqual([
      { rank: 0, labels: ["Any"] },
      { rank: 1, labels: ["480P"] },
      { rank: 2, labels: ["720P"] },
    ]);
  });

  it("returns undefined when no rank table exists", () => {
    expect(parseRankLadder("package silo.lib.other\n\nallow := true")).toBeUndefined();
  });
});
