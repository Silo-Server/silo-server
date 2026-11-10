import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { MediaItemUserState } from "@/api/types";
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
  it.each<[string, MediaItemUserState | undefined, string | null]>([
    ["a watched title", watched, "Watched"],
    ["a started series", started, "3 episodes left"],
    ["an untouched series", untouched, null],
    ["an unwatched movie", movie, null],
    ["no user state", undefined, null],
  ])("shows the right thing for %s", (_name, userState, expected) => {
    const { container } = render(<CardWatchStatus userState={userState} />);
    if (expected === null) {
      expect(container).toBeEmptyDOMElement();
    } else {
      expect(screen.getByRole("img", { name: expected })).toBeInTheDocument();
    }
  });
});
