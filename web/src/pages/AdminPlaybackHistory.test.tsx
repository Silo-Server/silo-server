import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AdminPlaybackHistoryItem } from "@/api/types";

const mocks = vi.hoisted(() => ({ rows: [] as AdminPlaybackHistoryItem[] }));

vi.mock("@/hooks/queries/admin/users", () => ({
  useAdminUsers: () => ({ data: [] }),
}));
vi.mock("@/hooks/queries/admin/history", () => ({
  useAdminPlaybackHistory: () => ({
    data: mocks.rows,
    isLoading: false,
    isFetching: false,
    error: null,
    refetch: vi.fn(),
  }),
  useAdminUserProfiles: () => ({ data: [] }),
}));

import AdminPlaybackHistory from "./AdminPlaybackHistory";

function makeRow(overrides: Partial<AdminPlaybackHistoryItem> = {}): AdminPlaybackHistoryItem {
  return {
    session_id: "session-movie",
    user_id: 1,
    username: "Example viewer",
    profile_id: "example-profile",
    profile_name: "Example profile",
    media_item_id: "movie-1",
    media_file_id: 10,
    media_title: "The Lantern Keeper",
    media_type: "movie",
    play_method: "direct_play",
    started_at: "2026-01-01T12:00:00Z",
    ended_at: "2026-01-01T13:00:00Z",
    watched_seconds: 3600,
    duration_seconds: 3600,
    completed: true,
    played_seconds: null,
    series_title: "",
    season_number: null,
    episode_number: null,
    ...overrides,
  };
}

function makeEpisode(overrides: Partial<AdminPlaybackHistoryItem> = {}) {
  return makeRow({
    session_id: "session-episode",
    media_item_id: "episode-204",
    media_title: "The Tide Turns",
    media_type: "episode",
    series_title: "Harbour Lights",
    season_number: 2,
    episode_number: 4,
    ...overrides,
  });
}

function renderPage(path = "/admin/history") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AdminPlaybackHistory />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  mocks.rows = [];
});
afterEach(cleanup);

describe("AdminPlaybackHistory media column", () => {
  it("leads an episode with its series title, then its code and episode title", () => {
    mocks.rows = [makeEpisode(), makeRow()];
    renderPage();

    const episode = screen.getByRole("link", { name: "Harbour Lights S02E04 · The Tide Turns" });
    expect(episode).toHaveAttribute("href", "/item/episode-204");
    const movie = screen.getByRole("link", { name: "The Lantern Keeper" });
    expect(movie).toHaveAttribute("href", "/item/movie-1");
  });

  it("shows whichever part of an episode is known", () => {
    mocks.rows = [
      makeEpisode({ session_id: "untitled", media_item_id: "episode-101", media_title: "" }),
      makeEpisode({
        session_id: "unnumbered",
        media_item_id: "episode-x",
        season_number: null,
        episode_number: null,
      }),
      makeEpisode({
        session_id: "pilot",
        media_item_id: "episode-pilot",
        season_number: 1,
        episode_number: 1,
        media_title: "Harbour Lights",
      }),
      // Without a series title the row keeps the episode title alone.
      makeEpisode({ session_id: "orphan", media_item_id: "episode-205", series_title: "" }),
    ];
    renderPage();

    expect(screen.getByRole("link", { name: "Harbour Lights S02E04" })).toHaveAttribute(
      "href",
      "/item/episode-101",
    );
    expect(screen.getByRole("link", { name: "Harbour Lights The Tide Turns" })).toHaveAttribute(
      "href",
      "/item/episode-x",
    );
    expect(screen.getByRole("link", { name: "Harbour Lights S01E01" })).toHaveAttribute(
      "href",
      "/item/episode-pilot",
    );
    expect(screen.getByRole("link", { name: "The Tide Turns" })).toHaveAttribute(
      "href",
      "/item/episode-205",
    );
  });

  it("names a filtered episode by its series in the item filter", () => {
    mocks.rows = [makeEpisode()];
    renderPage("/admin/history?media_item_id=episode-204");

    expect(screen.getByText("Item filter active").parentElement).toHaveTextContent(
      "Harbour Lights · S02E04 · The Tide Turns",
    );
  });
});
