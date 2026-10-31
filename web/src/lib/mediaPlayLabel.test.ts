// @vitest-environment node

import { describe, expect, it } from "vitest";

import { mediaPlayLabel } from "./mediaPlayLabel";

type PlayLabelItem = Parameters<typeof mediaPlayLabel>[0];

/** Only the fields the label reads are ever set; the rest stay absent. */
function item(overrides: Partial<PlayLabelItem>): PlayLabelItem {
  return { type: "movie", ...overrides } as PlayLabelItem;
}

/** A finished audiobook's user state is the only part of it the label reads. */
function played(): PlayLabelItem["user_state"] {
  return { played: true } as PlayLabelItem["user_state"];
}

describe("mediaPlayLabel", () => {
  it("plays a film or an episode, and reads a book", () => {
    expect(mediaPlayLabel(item({ type: "movie" }))).toBe("Play");
    expect(mediaPlayLabel(item({ type: "series" }))).toBe("Play");
    expect(mediaPlayLabel(item({ type: "episode" }))).toBe("Play");
    expect(mediaPlayLabel(item({ type: "ebook" }))).toBe("Read");
  });

  it("names an audiobook by whether it has been started", () => {
    expect(mediaPlayLabel(item({ type: "audiobook" }))).toBe("Listen");
    expect(mediaPlayLabel(item({ type: "audiobook", user_state: played() }))).toBe("Listen Again");
    // Part-way through, so it resumes rather than starting again.
    expect(
      mediaPlayLabel(item({ type: "audiobook", position_seconds: 600, duration_seconds: 6000 })),
    ).toBe("Resume");
    // At the end: nothing left to resume, and it has been listened to.
    expect(
      mediaPlayLabel(item({ type: "audiobook", position_seconds: 6000, duration_seconds: 6000 })),
    ).toBe("Listen");
  });

  it("follows the player for an audiobook that is already running", () => {
    const half = item({ type: "audiobook", position_seconds: 600, duration_seconds: 6000 });
    expect(mediaPlayLabel(half, true)).toBe("Pause");
    expect(mediaPlayLabel(half, false)).toBe("Resume");
  });

  it("keeps Play for a video whatever the player is doing", () => {
    // Only the audiobook player reports its state, and it must not reach a film.
    expect(mediaPlayLabel(item({ type: "movie" }), true)).toBe("Play");
  });
});
