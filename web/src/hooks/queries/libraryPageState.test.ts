// @vitest-environment node

import { describe, expect, it } from "vitest";

import { parseLibraryPageStatePreference } from "./libraryPageState";

describe("library page state preference helpers", () => {
  it("still parses the legacy JSON-string encoding", () => {
    expect(
      parseLibraryPageStatePreference(
        JSON.stringify({
          version: 1,
          libraries: {
            "7": { search: "tab=library&sort=year" },
          },
        }),
      ),
    ).toEqual({
      version: 1,
      libraries: {
        "7": { search: "tab=library&sort=year" },
      },
    });
  });

  it("treats null and undefined as an empty preference", () => {
    expect(parseLibraryPageStatePreference(null)).toEqual({ version: 1, libraries: {} });
    expect(parseLibraryPageStatePreference(undefined)).toEqual({ version: 1, libraries: {} });
  });

  it("ignores malformed, wrong-version, and non-string entries", () => {
    expect(parseLibraryPageStatePreference("not json")).toEqual({ version: 1, libraries: {} });
    expect(parseLibraryPageStatePreference({ version: 2, libraries: {} })).toEqual({
      version: 1,
      libraries: {},
    });
    expect(
      parseLibraryPageStatePreference({
        version: 1,
        libraries: {
          "7": { search: 42 },
          nope: { search: "tab=library" },
          "9": { search: "tab=collections" },
        },
      }),
    ).toEqual({
      version: 1,
      libraries: {
        "9": { search: "tab=collections" },
      },
    });
  });
});
