import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import SeasonAccordion from "./SeasonAccordion";

vi.mock("@/hooks/queries/episodes", () => ({
  useItemEpisodes: () => ({ data: { episodes: [] }, isLoading: false }),
}));

vi.mock("@/components/CardPlayOverlay", () => ({
  default: ({ contentId, title, size }: { contentId: string; title: string; size: string }) => (
    <a href={`/watch/${contentId}`} aria-label={`Play ${title}`} data-size={size} />
  ),
}));

describe("SeasonAccordion", () => {
  it("adds a compact play target without replacing the season detail link", () => {
    const markup = renderToStaticMarkup(
      <MemoryRouter>
        <SeasonAccordion
          seasons={[
            {
              content_id: "season-3",
              play_content_id: "episode-7",
              season_number: 3,
              is_specials: false,
              title: "Season 3",
              overview: "",
              air_date: null,
              episode_count: 8,
              poster_url: "/season.jpg",
              poster_thumbhash: "",
            },
          ]}
        />
      </MemoryRouter>,
    );

    expect(markup).toContain('href="/item/season-3"');
    expect(markup).toContain('href="/watch/episode-7"');
    expect(markup).toContain('aria-label="Play Season 3"');
    expect(markup).toContain('data-size="compact"');
  });
});
