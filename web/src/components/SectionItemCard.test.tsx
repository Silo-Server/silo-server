import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import SectionItemCard from "./SectionItemCard";

vi.mock("@/components/ViewTransitionLink", () => ({
  default: ({ children, to }: { children: ReactNode; to: string }) => <a href={to}>{children}</a>,
}));

vi.mock("@/components/MediaItemMenu", () => ({
  default: () => null,
}));

vi.mock("@/components/overlays/CardOverlays", () => ({
  default: () => null,
}));

vi.mock("@/hooks/useOverlayPrefs", () => ({
  useOverlayPrefs: () => ({ prefs: null }),
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
          type: "episode",
          title: "Dumbston Checks In",
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
  });
});
