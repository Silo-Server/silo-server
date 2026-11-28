import { describe, expect, it } from "vitest";
import { combinedEpisodeTitle, episodePartsDiffer, indexOfEpisodeRow } from "./episodeRange";

const row = (title: string, ...parts: string[]) => ({
  title,
  spanned_episodes: parts.map((partTitle, i) => ({
    content_id: `e${i + 2}`,
    episode_number: i + 2,
    title: partTitle,
  })),
});

describe("combinedEpisodeTitle", () => {
  it.each([
    [
      "numbered parts share their name",
      row("Of Course He's Dead (1)", "Of Course He's Dead (2)"),
      "Of Course He's Dead",
    ],
    [
      "Part N suffixes share their name",
      row("The Finale, Part 1", "The Finale, Part 2"),
      "The Finale",
    ],
    [
      "Part N after a dash or colon",
      row("Exodus: Part 1", "Exodus: Part 2", "Exodus: Part 3"),
      "Exodus",
    ],
    ["different names are joined", row("Pilot", "The Second One"), "Pilot / The Second One"],
    [
      "numbered parts with different names drop their numbers",
      row("Aliens of London (1)", "World War Three (2)"),
      "Aliens of London / World War Three",
    ],
    [
      "a number on only some parts stays",
      row("Pilot (1)", "Tabula Rasa"),
      "Pilot (1) / Tabula Rasa",
    ],
    ["an untitled part leaves the titled ones", row("Pilot", ""), "Pilot"],
    ["a single episode keeps its title", row("Pilot"), "Pilot"],
  ])("%s", (_name, episode, want) => {
    expect(combinedEpisodeTitle(episode)).toBe(want);
  });
});

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
