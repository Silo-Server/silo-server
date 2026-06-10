import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import WatchTonightCard from "./WatchTonightCard";

vi.mock("@/playback/watchPlaybackContext", () => ({
  useWatchPlaybackController: () => ({ startPlayback: vi.fn() }),
}));

describe("WatchTonightCard", () => {
  it("links ebook cards to the reader and shows percent read", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <WatchTonightCard
          item={{
            content_id: "ebook 1",
            type: "ebook",
            title: "A Reader",
            year: 2026,
            genres: ["Fantasy"],
            status: "matched",
            rating_imdb: null,
            overview: "Ebook overview",
            watch_tonight_source: "continue_watching",
            position_seconds: 0.57,
            duration_seconds: 1,
            progress_updated_at: "2026-06-07T00:00:00Z",
            poster_url: "/ebook-cover.jpg",
            poster_thumbhash: "",
            backdrop_url: "",
            backdrop_thumbhash: "",
            logo_url: "",
          }}
          onPlay={() => {}}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/item/ebook%201"');
    expect(markup).toContain('href="/reader/ebook/ebook%201"');
    expect(markup).toContain("57% read");
    expect(markup).not.toContain('href="/watch/ebook');
    expect(markup).not.toContain("0 min left");
  });

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
