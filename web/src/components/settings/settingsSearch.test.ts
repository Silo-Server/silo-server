import { describe, expect, it } from "vitest";

import { countSettingsSearchItems, filterSettingsSearchGroups } from "./settingsSearch";

const groups = [
  {
    label: "Server",
    items: [
      {
        label: "General",
        description: "Authentication and logging",
        keywords: ["access token", "refresh token", "log level"],
        settings: [{ label: "Quiet Subsystems" }],
      },
      {
        label: "Database",
        description: "Postgres and Redis",
        keywords: ["connection url", "pool"],
        settings: [{ label: "Pool Max Open" }],
      },
    ],
  },
  {
    label: "Playback",
    items: [
      {
        label: "Subtitles",
        description: "Skipping behavior, language, and style",
        keywords: ["forced subtitles", "captions"],
      },
    ],
  },
];

describe("settingsSearch", () => {
  it("returns all groups when the query is empty", () => {
    const filtered = filterSettingsSearchGroups(groups, "");

    expect(countSettingsSearchItems(filtered)).toBe(3);
    expect(filtered.map((group) => group.label)).toEqual(["Server", "Playback"]);
  });

  it("matches item labels, descriptions, and keywords", () => {
    expect(filterSettingsSearchGroups(groups, "redis")).toEqual([
      {
        label: "Server",
        items: [groups[0]!.items[1]],
      },
    ]);

    expect(filterSettingsSearchGroups(groups, "access token")).toEqual([
      {
        label: "Server",
        items: [groups[0]!.items[0]],
      },
    ]);

    expect(filterSettingsSearchGroups(groups, "forced")).toEqual([
      {
        label: "Playback",
        items: [groups[1]!.items[0]],
      },
    ]);
  });

  it("matches individual setting labels", () => {
    expect(filterSettingsSearchGroups(groups, "quiet subsystems")).toEqual([
      {
        label: "Server",
        items: [groups[0]!.items[0]],
      },
    ]);

    expect(filterSettingsSearchGroups(groups, "pool max open")).toEqual([
      {
        label: "Server",
        items: [groups[0]!.items[1]],
      },
    ]);
  });

  it("matches section labels by returning the full section", () => {
    const filtered = filterSettingsSearchGroups(groups, "server");

    expect(filtered).toEqual([groups[0]]);
    expect(countSettingsSearchItems(filtered)).toBe(2);
  });

  it("does not match short tokens from the middle of a word", () => {
    expect(filterSettingsSearchGroups(groups, "pin")).toEqual([]);
  });
});
