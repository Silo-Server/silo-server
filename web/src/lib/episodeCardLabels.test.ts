import { describe, expect, it } from "vitest";
import { buildSeasonCardLabel } from "./episodeCardLabels";

describe("buildSeasonCardLabel", () => {
  it("labels seasons and specials, and nothing else", () => {
    expect(buildSeasonCardLabel({ type: "season", season_number: 2 })).toBe("Season 2");
    expect(buildSeasonCardLabel({ type: "season", season_number: 0 })).toBe("Specials");
    expect(buildSeasonCardLabel({ type: "season", season_number: null })).toBe("Season");
    expect(buildSeasonCardLabel({ type: "series", season_number: 2 })).toBeNull();
  });
});
