import { describe, expect, it } from "vitest";
import { episodeOwnTitle } from "../itemDetailLayout";
import { episodeCardSpokenTitle, episodeCardTitleParts } from "./episodeCardText";

describe("episodeOwnTitle", () => {
  it("drops the server's English default for the same episode number", () => {
    expect(episodeOwnTitle(3, "Episode 3")).toBeUndefined();
    expect(episodeOwnTitle(3, "  Episode 3 ")).toBeUndefined();
    expect(episodeOwnTitle(3, "")).toBeUndefined();
    expect(episodeOwnTitle(3, undefined)).toBeUndefined();
  });

  it("keeps a real title, including one that names another episode number", () => {
    expect(episodeOwnTitle(3, "Pilot")).toBe("Pilot");
    expect(episodeOwnTitle(3, "Episode 4")).toBe("Episode 4");
  });
});

describe("episode card text with a stored default title", () => {
  const untitled = { season_number: 1, episode_number: 3, title: "Episode 3", air_date: null };

  it("shows the code alone", () => {
    expect(episodeCardTitleParts(untitled)).toEqual({ prefix: "S01E03" });
  });

  it("speaks the untitled label", () => {
    expect(episodeCardSpokenTitle(untitled)).toBe(
      episodeCardSpokenTitle({ ...untitled, title: "" }),
    );
  });

  it("shows and speaks the air date for a dated episode in air-date mode", () => {
    const dated = { ...untitled, air_date: "2026-02-18" };

    expect(episodeCardTitleParts(dated, "air_date")).toEqual({ prefix: "Feb 18, 2026" });
    expect(episodeCardSpokenTitle(dated, "air_date")).toBe("Feb 18, 2026");
  });

  it("shows and speaks the episode code for an undated episode in air-date mode", () => {
    expect(episodeCardTitleParts(untitled, "air_date")).toEqual({ prefix: "S01E03" });
    expect(episodeCardSpokenTitle(untitled, "air_date")).toBe("S01E03");
  });

  it("keeps a real title", () => {
    expect(episodeCardTitleParts({ ...untitled, title: "Pilot" })).toEqual({
      prefix: "S01E03 · ",
      title: "Pilot",
    });
  });
});
