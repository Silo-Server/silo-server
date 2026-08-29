import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import type { Season } from "@/api/types";
import SeasonCarousel from "./SeasonCarousel";

vi.mock("@/components/CardPlayOverlay", () => ({
  default: ({ contentId, title }: { contentId: string; title: string }) => (
    <a href={`/watch/${contentId}`} aria-label={`Play ${title}`} />
  ),
}));

function makeSeason(overrides: Partial<Season> = {}): Season {
  return {
    content_id: "season-1",
    season_number: 1,
    is_specials: false,
    title: "Season 1",
    overview: "",
    air_date: null,
    episode_count: 8,
    poster_url: "",
    poster_thumbhash: "",
    ...overrides,
  };
}

describe("SeasonCarousel", () => {
  it("renders an Embla viewport and container for the season cards", () => {
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={["/item/series-1"]}>
          <SeasonCarousel
            seasons={[
              makeSeason(),
              makeSeason({ content_id: "season-2", season_number: 2, title: "Season 2" }),
            ]}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain("embla__viewport");
    expect(markup).toContain("embla__container");
    expect(markup).toContain("overflow-hidden");
    expect(markup).not.toContain('data-slot="scroll-area"');
    expect(markup).not.toContain('data-slot="scroll-area-scrollbar"');
  });

  it("renders independent season detail and direct-play targets", () => {
    const markup = renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <SeasonCarousel seasons={[makeSeason({ play_content_id: "episode-2" })]} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(markup).toContain('href="/item/season-1"');
    expect(markup).toContain('href="/watch/episode-2"');
    expect(markup).toContain('aria-label="Play Season 1"');
  });
});
