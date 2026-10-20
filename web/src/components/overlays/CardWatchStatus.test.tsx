import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { MediaItemUserState } from "@/api/types";
import type { WatchStatusMode } from "@/lib/overlays";
import CardWatchStatus from "./CardWatchStatus";

const base = { is_favorite: false, in_watchlist: false };
const watched: MediaItemUserState = { ...base, played: true };
const started: MediaItemUserState = { ...base, played: false, watched_count: 4, unplayed_count: 3 };
const untouched: MediaItemUserState = {
  ...base,
  played: false,
  watched_count: 0,
  unplayed_count: 8,
};
const movie: MediaItemUserState = { ...base, played: false };

describe("CardWatchStatus", () => {
  it.each<[string, MediaItemUserState | undefined, WatchStatusMode, string | null]>([
    ["a watched title", watched, "remaining", "Watched"],
    ["a started series", started, "remaining", "3 episodes left"],
    ["an untouched series", untouched, "remaining", null],
    ["an unwatched movie", movie, "remaining", null],
    ["no user state", undefined, "remaining", null],
    ["a started series in watched-only mode", started, "watched", null],
    ["a watched title in watched-only mode", watched, "watched", "Watched"],
    ["a watched title with the corner off", watched, "off", null],
  ])("shows the right thing for %s", (_name, userState, mode, expected) => {
    const { container } = render(<CardWatchStatus userState={userState} mode={mode} />);
    if (expected === null) {
      expect(container).toBeEmptyDOMElement();
    } else {
      expect(screen.getByRole("img", { name: expected })).toBeInTheDocument();
    }
  });
});
