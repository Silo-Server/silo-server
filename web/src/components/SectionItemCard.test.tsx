import type { AnchorHTMLAttributes, ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import SectionItemCard from "./SectionItemCard";

vi.mock("@/components/ViewTransitionLink", () => ({
  /*
   * Every anchor prop is forwarded, not just children. A stub that drops
   * className or aria-label hides the very thing a test is asserting, and the
   * test then passes for the wrong reason.
   */
  default: ({
    children,
    to,
    ...rest
  }: { children: ReactNode; to: string } & AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={to} {...rest}>
      {children}
    </a>
  ),
}));

vi.mock("@/components/MediaItemMenu", () => ({
  default: () => null,
}));

vi.mock("@/components/CardPlayOverlay", () => ({
  default: ({ contentId, title }: { contentId: string; title: string }) => (
    <a href={`/watch/${contentId}`} aria-label={`Play ${title}`} />
  ),
}));

vi.mock("@/components/overlays/CardOverlays", () => ({
  default: () => null,
}));

// The tile's watchlist button writes through the query layer. This test is about
// what the card draws, so the write is stubbed rather than provided for.
vi.mock("@/hooks/queries/watchlist", () => ({
  useToggleWatchlist: () => ({ mutate: () => {}, isPending: false }),
}));

describe("SectionItemCard", () => {
  it("encodes item links while preserving library context", () => {
    const markup = renderToStaticMarkup(
      <SectionItemCard
        libraryId={12}
        item={{
          content_id: "ebook 1/isbn:978",
          type: "ebook",
          title: "A Reader",
          year: 2026,
          genres: ["Fantasy"],
          status: "matched",
          rating_imdb: null,
          overview: "Overview",
          poster_url: "",
          poster_thumbhash: "",
          backdrop_url: "",
          backdrop_thumbhash: "",
          logo_url: "",
        }}
      />,
    );

    expect(markup).toContain('href="/item/ebook%201%2Fisbn%3A978?libraryId=12"');
  });

  it("renders episode cards with series title, episode title, and episode code", () => {
    const markup = renderToStaticMarkup(
      <SectionItemCard
        item={{
          content_id: "episode-1",
          play_content_id: "episode-1",
          type: "episode",
          title: "Dumbston Checks In",
          series_id: "series-1",
          series_title: "American Dad!",
          season_number: 22,
          episode_number: 10,
          year: 2025,
          genres: ["Animation"],
          status: "matched",
          rating_imdb: 7.1,
          overview: "Overview",
          poster_url: "",
          poster_thumbhash: "",
          backdrop_url: "",
          backdrop_thumbhash: "",
          logo_url: "",
        }}
      />,
    );

    expect(markup).toContain("American Dad!");
    expect(markup).toContain("Dumbston Checks In");
    expect(markup).toContain("S22E10");
    expect(markup).toContain('href="/item/episode-1"');
    expect(markup).toContain('href="/item/series-1"');
    expect(markup).toContain('href="/watch/episode-1"');
    expect(markup).toContain('aria-label="Play American Dad!"');
  });

  it("renders premiere metadata when an upcoming event is present", () => {
    const markup = renderToStaticMarkup(
      <SectionItemCard
        item={{
          content_id: "series-1",
          type: "series",
          title: "Series A",
          year: 2024,
          genres: ["Drama"],
          status: "matched",
          rating_imdb: 8.2,
          overview: "Overview",
          poster_url: "",
          poster_thumbhash: "",
          backdrop_url: "",
          backdrop_thumbhash: "",
          logo_url: "",
          upcoming_event: {
            type: "season_premiere",
            air_date: "2026-04-08",
            air_time: "20:00",
            episode_title: "Back Again",
            season_number: 2,
            badges: ["season_premiere"],
          },
        }}
      />,
    );

    expect(markup).toContain("Series A");
    expect(markup).toContain("New Season");
    expect(markup).toContain("Season 2 · Back Again");
    expect(markup).toContain("Wed, Apr 8");
    expect(markup).toContain("8:00 PM");
    expect(markup).not.toContain('data-watched-indicator="icon-only"');
  });

  it("draws a ranked card's own numeral and holds the poster and caption clear of it", () => {
    const card = (rank: number) =>
      renderToStaticMarkup(
        <SectionItemCard
          rank={rank}
          rankedGutterClasses="pl-[130px]"
          rankedNumeralClasses="-left-[130px]"
          item={{
            content_id: "movie-1",
            type: "movie",
            title: "Heat",
            year: 1995,
            genres: [],
            status: "matched",
            rating_imdb: null,
            overview: "",
            poster_url: "",
            poster_thumbhash: "",
            backdrop_url: "",
            backdrop_thumbhash: "",
            logo_url: "",
          }}
        />,
      );

    const first = card(1);
    const fourth = card(4);

    expect(first).toContain('data-rank="1"');
    expect(fourth).toContain('data-rank="4"');
    // A different glyph, not the same box relabelled.
    expect(fourth).not.toBe(first);
    expect(fourth).toContain('viewBox="0 0 130 154"');

    // The caption clears the numeral, so a ranked row's titles still line up
    // with its posters. (The poster's own padding is asserted in
    // SectionRow.test, whose ViewTransitionLink is not stubbed out.)
    expect(fourth).toContain("pl-[130px]");

    // The numerals stop at ten: an eleventh card is drawn as an ordinary one.
    expect(card(11)).not.toContain("data-rank=");
  });

  it("draws the open tile's backdrop and logo, and pins the poster, only while open", () => {
    const card = (expanded: boolean) =>
      renderToStaticMarkup(
        <SectionItemCard
          expandable
          expanded={expanded}
          restingWidthClasses="w-[185px]"
          item={{
            content_id: "movie-1",
            play_content_id: "movie-1",
            type: "movie",
            title: "Heat",
            year: 1995,
            runtime: 102,
            genres: ["Drama"],
            content_rating: "M",
            status: "matched",
            rating_imdb: null,
            overview: "",
            poster_url: "",
            poster_thumbhash: "",
            backdrop_url: "/backdrop.jpg",
            backdrop_thumbhash: "",
            logo_url: "/logo.png",
          }}
        />,
      );

    const open = card(true);
    expect(open).toContain("media-card-featured-tile");
    expect(open).toContain('src="/backdrop.jpg"');
    expect(open).toContain('src="/logo.png"');

    // The poster holds its resting width whether the card is open or shut. That
    // pin has to outlast the open state: the card's width animates, the
    // artwork's height follows its width, and a poster that let go on collapse
    // would balloon to the grown width's height and push every row below it.
    expect(open).toContain("w-[185px]");

    // The tile's own body: the hero's buttons and its metadata row, at tile size.
    expect(open).toContain("pill-primary");
    expect(open).toContain("Add Heat to watchlist");
    expect(open).toContain("More info about Heat");
    expect(open).toContain("Drama");
    expect(open).toContain("1h 42m");

    // Shut, there is no tile, and so none of the body it carries.
    const closed = card(false);
    expect(closed).not.toContain("media-card-featured-tile");
    expect(closed).not.toContain("pill-primary");
    expect(closed).not.toContain("1h 42m");
    expect(closed).toContain("w-[185px]");
  });
});
