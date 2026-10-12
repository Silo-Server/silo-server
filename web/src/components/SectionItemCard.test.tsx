import type { ReactNode } from "react";
import { act } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import SectionItemCard from "./SectionItemCard";

const prefetchItemDetail = vi.hoisted(() => vi.fn());
const prefetchLibraryIds = vi.hoisted(() => [] as (number | undefined)[]);

vi.mock("@/hooks/queries/catalogRead", () => ({
  usePrefetchCatalogItemDetail: (libraryId?: number) => {
    prefetchLibraryIds.push(libraryId);
    return prefetchItemDetail;
  },
}));

vi.mock("@/components/ViewTransitionLink", () => ({
  default: ({ children, to }: { children: ReactNode; to: string }) => <a href={to}>{children}</a>,
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

  it("keeps series cards linked to the series destination", () => {
    const markup = renderToStaticMarkup(
      <SectionItemCard
        item={{
          content_id: "series-1",
          type: "series",
          title: "American Dad!",
          year: 2005,
          genres: ["Animation"],
          status: "matched",
          rating_imdb: 7.4,
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
    expect(markup).toContain('href="/item/series-1"');
    expect(markup).not.toContain("S22E10");
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
    expect(markup).toContain("Season Premiere");
    expect(markup).toContain("Season 2 · Back Again");
    expect(markup).toContain("Wed, Apr 8");
    expect(markup).toContain("8:00 PM");
    expect(markup).not.toContain('data-watched-indicator="icon-only"');
  });

  it("warms the detail the poster opens, in its library, after a hover dwell", () => {
    vi.useFakeTimers();
    try {
      prefetchItemDetail.mockClear();
      prefetchLibraryIds.length = 0;
      render(
        <SectionItemCard
          libraryId={3}
          item={{
            content_id: "movie-2",
            type: "movie",
            title: "Arrival",
            year: 2016,
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
      const card = screen.getAllByRole("link")[0]!.closest(".media-card")!;

      fireEvent.pointerEnter(card, { pointerType: "mouse" });
      act(() => vi.advanceTimersByTime(100));

      expect(prefetchItemDetail).toHaveBeenCalledExactlyOnceWith("movie-2");
      expect(prefetchLibraryIds.at(-1)).toBe(3);
    } finally {
      vi.useRealTimers();
    }
  });
});
