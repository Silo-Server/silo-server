// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { AdminSession } from "@/api/types";
import { getPlaybackSessionSubtitle, getPlaybackSessionTitle } from "./adminActivityPresentation";

function makeSession(overrides: Partial<AdminSession> = {}): AdminSession {
  return {
    session_id: "s1",
    user_id: 1,
    username: "viewer",
    profile_id: "p1",
    media_file_id: 42,
    requested_media_file_id: 42,
    media_title: "8:00 A.M.",
    ...overrides,
  } as AdminSession;
}

const episode = {
  media_type: "episode",
  series_title: "The Pitt",
  season_number: 1,
  episode_number: 2,
} as const;

describe("playback session title", () => {
  it("leads an episode with its show and puts the episode underneath", () => {
    const session = makeSession(episode);
    expect(getPlaybackSessionTitle(session)).toBe("The Pitt");
    expect(getPlaybackSessionSubtitle(session)).toBe("S1E2 · 8:00 A.M.");
  });

  it("shows only the episode code when the episode has no title", () => {
    const session = makeSession({ ...episode, media_title: "" });
    expect(getPlaybackSessionTitle(session)).toBe("The Pitt");
    expect(getPlaybackSessionSubtitle(session)).toBe("S1E2");
  });

  it("keeps a movie's own title and gives it no subtitle", () => {
    const session = makeSession({ media_type: "movie", media_title: "Heat" });
    expect(getPlaybackSessionTitle(session)).toBe("Heat");
    expect(getPlaybackSessionSubtitle(session)).toBeNull();
  });
});
