import { describe, expect, it } from "vitest";
import { episodePartsDiffer, indexOfEpisodeRow } from "./episodeRange";

describe("episodePartsDiffer", () => {
  const parts = (overview: string, ...rest: string[]) => ({
    overview,
    spanned_episodes: rest.map((partOverview, i) => ({
      content_id: `e${i + 2}`,
      episode_number: i + 2,
      title: "",
      overview: partOverview,
    })),
  });
  it.each([
    ["different descriptions", parts("Rose is taken home.", "Time is running out."), true],
    ["the same description", parts("Charlie is alive.", "Charlie is alive. "), false],
    ["no descriptions", parts("", ""), false],
    ["one part described", parts("Rose is taken home.", ""), true],
    ["a single episode", parts("Rose is taken home."), false],
  ])("%s", (_name, episode, want) => {
    expect(episodePartsDiffer(episode)).toBe(want);
  });
});

describe("indexOfEpisodeRow", () => {
  const rows = [
    { seasonNumber: 1, episodeNumber: 3 },
    { seasonNumber: 1, episodeNumber: 4, episodeNumberEnd: 5 },
    { seasonNumber: 1, episodeNumber: 6 },
  ];
  it.each([
    ["an episode's own row", 3, 0],
    ["the first episode of a multi-episode row", 4, 1],
    ["a later episode a multi-episode row plays", 5, 1],
    ["the row after a multi-episode row", 6, 2],
    ["an episode no row holds", 7, -1],
  ])("%s", (_name, episode, want) => {
    expect(indexOfEpisodeRow(rows, 1, episode)).toBe(want);
  });
});
