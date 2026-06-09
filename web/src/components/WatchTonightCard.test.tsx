import { MemoryRouter } from "react-router";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import WatchTonightCard from "./WatchTonightCard";

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));

describe("WatchTonightCard", () => {
  it("uses listen copy for audiobook play links", () => {
    render(
      <MemoryRouter>
        <WatchTonightCard
          onPlay={() => {}}
          item={{
            content_id: "book-1",
            type: "audiobook",
            title: "Book One",
            year: 2026,
            genres: [],
            status: "matched",
            rating_imdb: null,
            overview: "",
            poster_url: "",
            poster_thumbhash: "",
            backdrop_url: "",
            backdrop_thumbhash: "",
            logo_url: "",
            watch_tonight_source: "recommendation",
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.getByRole("link", { name: "Listen Book One" })).toHaveAttribute(
      "href",
      "/item/book-1?play=1",
    );
  });
});
