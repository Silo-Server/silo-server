import { describe, expect, it } from "vitest";

import { isEpisodeUnwatched } from "./episodeSpoilers";

describe("isEpisodeUnwatched", () => {
  it("treats an episode with no watch state as unwatched", () => {
    expect(isEpisodeUnwatched(undefined)).toBe(true);
    expect(isEpisodeUnwatched(null)).toBe(true);
    expect(isEpisodeUnwatched({ played: false })).toBe(true);
  });

  it("reveals played and started episodes", () => {
    expect(isEpisodeUnwatched({ played: true })).toBe(false);
    expect(isEpisodeUnwatched({ played: false, is_in_progress: true })).toBe(false);
    expect(isEpisodeUnwatched({ played: false, position_seconds: 12 })).toBe(false);
  });
});
