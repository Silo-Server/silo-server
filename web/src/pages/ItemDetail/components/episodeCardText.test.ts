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

  it("keeps a real title", () => {
    expect(episodeCardTitleParts({ ...untitled, title: "Pilot" })).toEqual({
      prefix: "S01E03 · ",
      title: "Pilot",
    });
  });
});
