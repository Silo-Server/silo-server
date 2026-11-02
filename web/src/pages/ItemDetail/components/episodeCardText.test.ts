// @vitest-environment node

import { afterEach, describe, expect, it } from "vitest";
import { setDateTimeFormatPreferences } from "@/lib/datetime";
import { episodeCardMetadata, episodeCardSpokenTitle, episodeCardTitle } from "./episodeCardText";

const SEPARATOR = "  ·  ";

afterEach(() => {
  setDateTimeFormatPreferences({ dateFormat: "auto", timeFormat: "auto" });
});

describe("episodeCardTitle", () => {
  it("joins the zero-padded code and title, or shows the code alone", () => {
    expect(episodeCardTitle({ season_number: 1, episode_number: 2, title: "Pilot" })).toBe(
      "S01E02 · Pilot",
    );
    expect(episodeCardTitle({ season_number: 0, episode_number: 5, title: "  " })).toBe("S00E05");
    expect(episodeCardTitle({ season_number: 2021, episode_number: 3, title: "" })).toBe(
      "S2021E03",
    );
  });
});

describe("episodeCardSpokenTitle", () => {
  it("spells out the season and episode for screen readers", () => {
    expect(episodeCardSpokenTitle({ season_number: 1, episode_number: 2, title: "Pilot" })).toBe(
      "Season 1, Episode 2, Pilot",
    );
    expect(episodeCardSpokenTitle({ season_number: 0, episode_number: 5, title: "" })).toBe(
      "Specials, Episode 5",
    );
  });
});

describe("episodeCardMetadata", () => {
  it("shows the air date and runtime with the shared separator", () => {
    setDateTimeFormatPreferences({ dateFormat: "MM/DD/YYYY", timeFormat: "auto" });
    expect(episodeCardMetadata({ air_date: "2026-02-18", runtime: 52 })).toBe(
      `Feb 18, 2026${SEPARATOR}52m`,
    );
    expect(episodeCardMetadata({ air_date: "2026-02-18", runtime: 65 })).toBe(
      `Feb 18, 2026${SEPARATOR}1h 5m`,
    );
  });

  it("shows whichever part is known, or nothing", () => {
    setDateTimeFormatPreferences({ dateFormat: "MM/DD/YYYY", timeFormat: "auto" });
    expect(episodeCardMetadata({ air_date: null, runtime: 52 })).toBe("52m");
    expect(episodeCardMetadata({ air_date: "2026-02-18", runtime: 0 })).toBe("Feb 18, 2026");
    expect(episodeCardMetadata({ air_date: null, runtime: 0 })).toBeNull();
  });
});
