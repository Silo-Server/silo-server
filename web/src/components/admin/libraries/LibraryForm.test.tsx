import { describe, expect, it } from "vitest";

import { contentLevelsForType } from "./LibraryForm";

describe("contentLevelsForType", () => {
  it("maps ebook libraries to the ebook metadata content level", () => {
    expect(contentLevelsForType("ebooks")).toEqual(["ebook"]);
    expect(contentLevelsForType("ebook")).toEqual(["ebook"]);
  });

  it("includes ebook metadata providers for mixed libraries", () => {
    expect(contentLevelsForType("mixed")).toEqual([
      "movie",
      "series",
      "season",
      "episode",
      "audiobook",
      "ebook",
    ]);
  });
});
