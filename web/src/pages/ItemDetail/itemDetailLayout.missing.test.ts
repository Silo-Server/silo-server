import { describe, expect, it } from "vitest";
import type { Season } from "@/api/types";
import { formatSeasonMeta } from "./itemDetailLayout";

function season(episodeCount: number, missing?: number): Season {
  return {
    content_id: "series:x-S01",
    season_number: 1,
    is_specials: false,
    title: "Season 1",
    overview: "",
    air_date: null,
    episode_count: episodeCount,
    poster_url: "",
    poster_thumbhash: "",
    missing_episode_count: missing,
  };
}

describe("formatSeasonMeta with missing episodes", () => {
  it("counts only the library when nothing is missing", () => {
    expect(formatSeasonMeta(season(10))).toBe("10 episodes");
    expect(formatSeasonMeta(season(10, 0))).toBe("10 episodes");
  });

  it("shows what the library holds out of the whole season", () => {
    expect(formatSeasonMeta(season(8, 2))).toBe("8 of 10 episodes");
  });

  it("names a season the library lacks entirely", () => {
    expect(formatSeasonMeta(season(0, 1))).toBe("Not in library · 1 episode");
  });

  it("names a season still to air", () => {
    const now = new Date("2026-10-22T12:00:00Z");
    expect(formatSeasonMeta({ ...season(0, 8), air_date: "2027-01-10" }, now)).toBe(
      "Upcoming · 8 episodes",
    );
    expect(formatSeasonMeta({ ...season(0, 8), air_date: "2025-01-10" }, now)).toBe(
      "Not in library · 8 episodes",
    );
  });
});
